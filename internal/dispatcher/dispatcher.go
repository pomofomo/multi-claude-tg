// Package dispatcher is the heart of TRD: it owns the Telegram long-poll,
// the WS server, the process manager, and the pub/sub between them.
package dispatcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pomofomo/multi-claude-tg/internal/config"
	"github.com/pomofomo/multi-claude-tg/internal/storage"
	"github.com/pomofomo/multi-claude-tg/internal/telegram"
	"github.com/pomofomo/multi-claude-tg/internal/tmuxmgr"
	"github.com/pomofomo/multi-claude-tg/internal/ws"
)

// Options for Run.
type Options struct {
	TelegramToken string
	Port          int
	Logger        *slog.Logger
	// HealthInterval is how often the health loop wakes up. 0 = 30s default.
	HealthInterval time.Duration
	// AttachDir is where downloaded Telegram attachments are written.
	// Defaults to ~/.trd/attachments.
	AttachDir string
}

// Dispatcher glues the subsystems together.
type Dispatcher struct {
	opts   Options
	logger *slog.Logger
	tg     *telegram.Client
	store  *storage.Store
	server *ws.Server

	// live WS conns keyed by instance_id.
	mu    sync.RWMutex
	conns map[string]*liveConn

	// pending download responses, keyed by (instance_id+req_id) -> callback chan.
	pendingMu sync.Mutex
	pending   map[string]chan ws.Frame
}

type liveConn struct {
	conn    *ws.Conn
	inbound chan ws.Frame // dispatcher -> plugin
}

// New builds a Dispatcher, opening the DB and constructing the WS server.
func New(opts Options) (*Dispatcher, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HealthInterval == 0 {
		opts.HealthInterval = 30 * time.Second
	}
	if opts.Port == 0 {
		opts.Port = 7777
	}
	if opts.TelegramToken == "" {
		return nil, errors.New("telegram token is required")
	}

	if err := config.EnsureRoot(); err != nil {
		return nil, fmt.Errorf("create ~/.trd: %w", err)
	}
	dbPath, err := config.StateDBPath()
	if err != nil {
		return nil, err
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}
	if opts.AttachDir == "" {
		root, _ := config.Root()
		opts.AttachDir = filepath.Join(root, "attachments")
	}
	if err := os.MkdirAll(opts.AttachDir, 0o700); err != nil {
		return nil, err
	}

	d := &Dispatcher{
		opts:    opts,
		logger:  opts.Logger,
		tg:      telegram.New(opts.TelegramToken),
		store:   store,
		conns:   map[string]*liveConn{},
		pending: map[string]chan ws.Frame{},
	}
	d.server = ws.New(fmt.Sprintf("127.0.0.1:%d", opts.Port), opts.Logger, d)
	return d, nil
}

// Close flushes state.
func (d *Dispatcher) Close() error { return d.store.Close() }

// --- ws.Handler implementation ---

// AuthSecret looks up an instance by secret.
func (d *Dispatcher) AuthSecret(secret string) (string, int64, int, bool) {
	inst, err := d.store.BySecret(secret)
	if err != nil || inst == nil {
		return "", 0, 0, false
	}
	return inst.InstanceID, inst.ChatID, inst.TopicID, true
}

// Register binds a WS conn to an instance. Returns a channel the writer should drain.
func (d *Dispatcher) Register(instanceID string, conn *ws.Conn) <-chan ws.Frame {
	ch := make(chan ws.Frame, 64)
	d.mu.Lock()
	// If there's an existing conn, close its channel first.
	if old, ok := d.conns[instanceID]; ok {
		close(old.inbound)
	}
	d.conns[instanceID] = &liveConn{conn: conn, inbound: ch}
	d.mu.Unlock()
	d.logger.Info("channel connected", "instance", instanceID)
	return ch
}

// Unregister removes the binding if it still points at conn.
func (d *Dispatcher) Unregister(instanceID string, conn *ws.Conn) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if live, ok := d.conns[instanceID]; ok && live.conn == conn {
		close(live.inbound)
		delete(d.conns, instanceID)
		d.logger.Info("channel disconnected", "instance", instanceID)
	}
}

// OnOutbound handles a plugin->server frame.
func (d *Dispatcher) OnOutbound(instanceID string, frame ws.Frame) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch frame.Type {
	case "reply":
		inst, _ := d.store.Get(instanceID)
		if inst == nil {
			d.logger.Warn("reply for unknown instance", "instance", instanceID)
			return
		}
		chatID := inst.ChatID
		threadID := inst.TopicID
		if frame.Text != "" {
			_, err := d.tg.SendMessage(ctx, telegram.SendMessageParams{
				ChatID:           chatID,
				MessageThreadID:  threadID,
				Text:             frame.Text,
				ReplyToMessageID: frame.ReplyTo,
			})
			if err != nil {
				d.logger.Warn("sendMessage failed", "err", err)
			}
		}
		for _, path := range frame.Files {
			if _, err := d.tg.SendDocument(ctx, chatID, threadID, path, ""); err != nil {
				d.logger.Warn("sendDocument failed", "path", path, "err", err)
			}
		}
	case "react":
		if err := d.tg.SetReaction(ctx, frame.ChatID, frame.MessageID, frame.Emoji); err != nil {
			d.logger.Warn("setReaction failed", "err", err)
		}
	case "edit":
		if err := d.tg.EditMessageText(ctx, telegram.EditMessageTextParams{
			ChatID:    frame.ChatID,
			MessageID: frame.MessageID,
			Text:      frame.Text,
		}); err != nil {
			d.logger.Warn("editMessageText failed", "err", err)
		}
	case "download":
		path, err := d.tg.DownloadFile(ctx, frame.FileID, d.opts.AttachDir)
		resp := ws.Frame{Type: "download_result", ReqID: frame.ReqID, Path: path}
		if err != nil {
			resp.Err = err.Error()
		}
		d.sendTo(instanceID, resp)
	case "hello":
		// Informational.
	default:
		d.logger.Warn("unknown frame type", "type", frame.Type)
	}
}

// sendTo pushes a frame to the given instance's WS writer channel. Drops if no conn.
func (d *Dispatcher) sendTo(instanceID string, frame ws.Frame) {
	d.mu.RLock()
	live, ok := d.conns[instanceID]
	d.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case live.inbound <- frame:
	default:
		d.logger.Warn("inbound full, dropping", "instance", instanceID)
	}
}

// --- Telegram long-poll and command handling ---

// Run starts the WS server and Telegram long-poll. Blocks until ctx is canceled.
func (d *Dispatcher) Run(ctx context.Context) error {
	// 1. Relaunch any running/stopped instances that have a tmux session expected.
	if err := d.resumeInstances(); err != nil {
		d.logger.Warn("resume instances", "err", err)
	}

	// 2. Start WS server.
	go func() {
		if err := d.server.ListenAndServe(ctx); err != nil {
			d.logger.Error("ws server", "err", err)
		}
	}()

	// 3. Start health loop.
	go d.healthLoop(ctx)

	// 4. Long-poll Telegram.
	return d.pollLoop(ctx)
}

func (d *Dispatcher) pollLoop(ctx context.Context) error {
	me, err := d.tg.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("getMe: %w", err)
	}
	d.logger.Info("telegram bot online", "username", me.Username)

	offset := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		updates, err := d.tg.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.logger.Warn("getUpdates failed", "err", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.Message != nil {
				d.handleMessage(ctx, u.Message)
			}
		}
	}
}

func (d *Dispatcher) handleMessage(ctx context.Context, m *telegram.Message) {
	if m.Chat.Type != "supergroup" || !m.Chat.IsForum {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "TRD requires a forum supergroup (topics enabled). This chat is "+m.Chat.Type+".")
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}

	// Strip bot mentions like "@mybot" from slash commands: "/start@mybot foo" -> "/start foo"
	if strings.HasPrefix(text, "/") {
		if idx := strings.Index(text, " "); idx > 0 {
			cmd := text[:idx]
			rest := text[idx+1:]
			if at := strings.Index(cmd, "@"); at > 0 {
				cmd = cmd[:at]
			}
			text = cmd + " " + rest
		} else if at := strings.Index(text, "@"); at > 0 {
			text = text[:at]
		}
	}

	switch {
	case strings.HasPrefix(text, "/start "):
		arg := strings.TrimSpace(strings.TrimPrefix(text, "/start"))
		d.cmdStart(ctx, m, arg)
	case text == "/start":
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "Usage: /start <ssh-git-url>")
	case text == "/stop":
		d.cmdStop(ctx, m)
	case text == "/restart":
		d.cmdRestart(ctx, m)
	case text == "/status":
		d.cmdStatus(ctx, m)
	case text == "/forget":
		d.cmdForget(ctx, m)
	default:
		d.routeToInstance(ctx, m, text)
	}
}

// --- commands ---

func (d *Dispatcher) cmdStart(ctx context.Context, m *telegram.Message, repoURL string) {
	if repoURL == "" {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "Usage: /start <ssh-git-url>")
		return
	}
	existing, err := d.store.ByTopic(m.Chat.ID, m.MessageThreadID)
	if err != nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "internal error: "+err.Error())
		return
	}
	if existing != nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID,
			fmt.Sprintf("This topic is already bound to %s (state=%s). Use /stop first.", existing.RepoURL, existing.State))
		return
	}

	instID := uuid.NewString()
	secret, err := randomHex(32)
	if err != nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "failed to generate secret: "+err.Error())
		return
	}
	reposDir, _ := config.ReposDir()
	repoPath := filepath.Join(reposDir, instID)

	d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "Cloning "+repoURL+"…")
	cloneCtx, cloneCancel := context.WithTimeout(ctx, 5*time.Minute)
	out, err := exec.CommandContext(cloneCtx, "git", "clone", repoURL, repoPath).CombinedOutput()
	cloneCancel()
	if err != nil {
		_ = os.RemoveAll(repoPath)
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "clone failed:\n"+truncate(string(out), 1500))
		return
	}

	cfg := config.RepoConfig{
		InstanceID:     instID,
		Secret:         secret,
		DispatcherPort: d.opts.Port,
	}
	if err := config.WriteRepoConfig(repoPath, cfg); err != nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "failed to write config: "+err.Error())
		return
	}
	_ = config.EnsureGitignore(repoPath)
	if err := writeMCPConfig(repoPath); err != nil {
		d.logger.Warn("write .mcp.json", "err", err)
	}

	inst := storage.Instance{
		InstanceID: instID,
		ChatID:     m.Chat.ID,
		TopicID:    m.MessageThreadID,
		RepoURL:    repoURL,
		RepoPath:   repoPath,
		Secret:     secret,
		State:      storage.StateRunning,
	}
	if err := d.store.Put(inst); err != nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "failed to persist: "+err.Error())
		return
	}

	if err := d.launchTmux(inst); err != nil {
		inst.State = storage.StateFailed
		_ = d.store.Put(inst)
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "failed to launch tmux: "+err.Error())
		return
	}
	d.sendText(ctx, m.Chat.ID, m.MessageThreadID,
		fmt.Sprintf("Ready. Instance %s running in tmux session %s.", instID[:8], tmuxmgr.SessionName(instID)))
}

func (d *Dispatcher) cmdStop(ctx context.Context, m *telegram.Message) {
	inst, _ := d.store.ByTopic(m.Chat.ID, m.MessageThreadID)
	if inst == nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "no instance bound to this topic")
		return
	}
	if err := tmuxmgr.KillSession(tmuxmgr.SessionName(inst.InstanceID)); err != nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "kill failed: "+err.Error())
		return
	}
	inst.State = storage.StateStopped
	_ = d.store.Put(*inst)
	d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "stopped")
}

func (d *Dispatcher) cmdRestart(ctx context.Context, m *telegram.Message) {
	inst, _ := d.store.ByTopic(m.Chat.ID, m.MessageThreadID)
	if inst == nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "no instance bound to this topic")
		return
	}
	_ = tmuxmgr.KillSession(tmuxmgr.SessionName(inst.InstanceID))
	if err := d.launchTmux(*inst); err != nil {
		inst.State = storage.StateFailed
		_ = d.store.Put(*inst)
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "restart failed: "+err.Error())
		return
	}
	inst.State = storage.StateRunning
	inst.FailCount = 0
	_ = d.store.Put(*inst)
	d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "restarted")
}

func (d *Dispatcher) cmdStatus(ctx context.Context, m *telegram.Message) {
	inst, _ := d.store.ByTopic(m.Chat.ID, m.MessageThreadID)
	if inst == nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "no instance bound to this topic")
		return
	}
	alive := tmuxmgr.HasSession(tmuxmgr.SessionName(inst.InstanceID))
	d.mu.RLock()
	_, connected := d.conns[inst.InstanceID]
	d.mu.RUnlock()
	msg := fmt.Sprintf(
		"instance: %s\nrepo: %s\npath: %s\nstate: %s\ntmux: %v\nchannel: %v\nfail_count: %d",
		inst.InstanceID[:8], inst.RepoURL, inst.RepoPath, inst.State, alive, connected, inst.FailCount,
	)
	d.sendText(ctx, m.Chat.ID, m.MessageThreadID, msg)
}

func (d *Dispatcher) cmdForget(ctx context.Context, m *telegram.Message) {
	inst, _ := d.store.ByTopic(m.Chat.ID, m.MessageThreadID)
	if inst == nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "no instance bound to this topic")
		return
	}
	_ = tmuxmgr.KillSession(tmuxmgr.SessionName(inst.InstanceID))
	if err := d.store.Delete(inst.InstanceID); err != nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "forget failed: "+err.Error())
		return
	}
	d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "forgotten. repo files at "+inst.RepoPath+" kept on disk.")
}

// routeToInstance forwards a non-command message to the bound instance's channel plugin.
func (d *Dispatcher) routeToInstance(ctx context.Context, m *telegram.Message, text string) {
	inst, _ := d.store.ByTopic(m.Chat.ID, m.MessageThreadID)
	if inst == nil {
		// Not bound — silently ignore so the bot doesn't spam every chat it's in.
		return
	}
	if inst.State != storage.StateRunning {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "instance state is "+string(inst.State)+"; use /restart")
		return
	}
	user := ""
	if m.From != nil {
		user = m.From.Username
		if user == "" {
			user = m.From.FirstName
		}
	}
	frame := ws.Frame{
		Type:      "message",
		ChatID:    m.Chat.ID,
		MessageID: m.MessageID,
		ThreadID:  m.MessageThreadID,
		User:      user,
		Text:      text,
		TS:        m.Date,
	}
	if m.Document != nil {
		frame.AttachmentFileID = m.Document.FileID
		frame.AttachmentName = m.Document.FileName
	} else if len(m.Photo) > 0 {
		// Largest photo is last.
		ph := m.Photo[len(m.Photo)-1]
		frame.AttachmentFileID = ph.FileID
	}
	d.sendTo(inst.InstanceID, frame)
}

// --- internals ---

func (d *Dispatcher) launchTmux(inst storage.Instance) error {
	name := tmuxmgr.SessionName(inst.InstanceID)
	if tmuxmgr.HasSession(name) {
		return nil
	}
	cfgPath := filepath.Join(inst.RepoPath, ".trd", "config.json")
	env := []string{
		"TRD_CONFIG=" + cfgPath,
		"TRD_INSTANCE_ID=" + inst.InstanceID,
	}
	// The channel plugin is discovered via the repo's .mcp.json we wrote at clone time.
	cmd := "claude --dangerously-skip-permissions --dangerously-load-development-channels server:trd-channel"
	return tmuxmgr.NewSession(name, inst.RepoPath, cmd, env)
}

func (d *Dispatcher) resumeInstances() error {
	all, err := d.store.All()
	if err != nil {
		return err
	}
	for _, inst := range all {
		if inst.State != storage.StateRunning {
			continue
		}
		if tmuxmgr.HasSession(tmuxmgr.SessionName(inst.InstanceID)) {
			continue
		}
		d.logger.Info("relaunching instance", "instance", inst.InstanceID)
		if err := d.launchTmux(inst); err != nil {
			d.logger.Warn("relaunch failed", "instance", inst.InstanceID, "err", err)
			inst.State = storage.StateFailed
			inst.FailCount++
			_ = d.store.Put(inst)
		}
	}
	return nil
}

func (d *Dispatcher) healthLoop(ctx context.Context) {
	t := time.NewTicker(d.opts.HealthInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.checkHealth(ctx)
		}
	}
}

func (d *Dispatcher) checkHealth(ctx context.Context) {
	all, err := d.store.All()
	if err != nil {
		return
	}
	for _, inst := range all {
		if inst.State != storage.StateRunning {
			continue
		}
		if tmuxmgr.HasSession(tmuxmgr.SessionName(inst.InstanceID)) {
			continue
		}
		d.logger.Warn("session dead, restarting", "instance", inst.InstanceID, "fails", inst.FailCount)
		if inst.FailCount >= 3 {
			inst.State = storage.StateFailed
			_ = d.store.Put(inst)
			d.sendText(ctx, inst.ChatID, inst.TopicID,
				"Instance failed to start after 3 attempts. Use /restart to retry.")
			continue
		}
		if err := d.launchTmux(inst); err != nil {
			inst.FailCount++
			_ = d.store.Put(inst)
			d.logger.Warn("restart failed", "err", err)
			continue
		}
		inst.FailCount = 0
		_ = d.store.Put(inst)
	}
}

func (d *Dispatcher) sendText(ctx context.Context, chatID int64, threadID int, text string) {
	_, err := d.tg.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:          chatID,
		MessageThreadID: threadID,
		Text:            text,
	})
	if err != nil {
		d.logger.Warn("sendText failed", "err", err)
	}
}

// Logs returns the captured tmux pane content for the topic's instance.
func (d *Dispatcher) Logs(chatID int64, threadID int) (string, error) {
	inst, _ := d.store.ByTopic(chatID, threadID)
	if inst == nil {
		return "", errors.New("no instance for topic")
	}
	return tmuxmgr.CapturePane(tmuxmgr.SessionName(inst.InstanceID))
}

// --- helpers ---

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}

// writeMCPConfig drops a .mcp.json into the repo so Claude finds the channel plugin.
//
// Resolution order:
//  1. $TRD_CHANNEL_ENTRY set to an absolute path → `bun run <path>` (dev install)
//  2. $TRD_CHANNEL_ENTRY set to anything else   → `bun run $TRD_CHANNEL_ENTRY`
//  3. default                                    → `trd-channel` (npm bin on PATH)
func writeMCPConfig(repoPath string) error {
	mcpPath := filepath.Join(repoPath, ".mcp.json")
	entry := os.Getenv("TRD_CHANNEL_ENTRY")
	var command string
	var args []string
	if entry != "" {
		command = "bun"
		args = []string{"run", entry}
	} else {
		command = "trd-channel"
		args = []string{}
	}
	argsJSON, _ := json.Marshal(args)
	content := fmt.Sprintf(`{
  "mcpServers": {
    "trd-channel": {
      "command": %q,
      "args": %s
    }
  }
}
`, command, string(argsJSON))
	return os.WriteFile(mcpPath, []byte(content), 0o644)
}
