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
	"github.com/pomofomo/multi-claude-tg/internal/media"
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
	media  media.Config

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

// InstanceInfo is the enriched instance data returned by the API, adding
// runtime state (WS connection, tmux liveness) to the stored Instance.
type InstanceInfo struct {
	storage.Instance
	Connected bool `json:"connected"`
	TmuxAlive bool `json:"tmux_alive"`
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

	mediaCfg := media.ConfigFromEnv()
	d := &Dispatcher{
		opts:    opts,
		logger:  opts.Logger,
		tg:      telegram.New(opts.TelegramToken),
		store:   store,
		media:   mediaCfg,
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
		d.logger.Info("claude->tg reply",
			"instance", shortID(instanceID),
			"chat", chatID, "thread", threadID,
			"reply_to", frame.ReplyTo,
			"text_len", len(frame.Text), "text", preview(frame.Text),
			"files", len(frame.Files),
		)
		if frame.Text != "" {
			sent, err := d.tg.SendMessage(ctx, telegram.SendMessageParams{
				ChatID:           chatID,
				MessageThreadID:  threadID,
				Text:             frame.Text,
				ReplyToMessageID: frame.ReplyTo,
			})
			if err != nil {
				d.logger.Warn("tg sendMessage failed", "instance", shortID(instanceID), "err", err)
			} else {
				d.logger.Info("tg sendMessage ok", "instance", shortID(instanceID), "msg_id", sent.MessageID)
			}
		}
		for _, path := range frame.Files {
			if err := d.sendFileSmartly(ctx, chatID, threadID, path, instanceID); err != nil {
				d.logger.Warn("tg send file failed", "instance", shortID(instanceID), "path", path, "err", err)
			}
		}
	case "react":
		d.logger.Info("claude->tg react",
			"instance", shortID(instanceID),
			"chat", frame.ChatID, "msg_id", frame.MessageID, "emoji", frame.Emoji,
		)
		if err := d.tg.SetReaction(ctx, frame.ChatID, frame.MessageID, frame.Emoji); err != nil {
			d.logger.Warn("tg setReaction failed", "instance", shortID(instanceID), "err", err)
		}
	case "edit":
		d.logger.Info("claude->tg edit",
			"instance", shortID(instanceID),
			"chat", frame.ChatID, "msg_id", frame.MessageID,
			"text_len", len(frame.Text), "text", preview(frame.Text),
		)
		if err := d.tg.EditMessageText(ctx, telegram.EditMessageTextParams{
			ChatID:    frame.ChatID,
			MessageID: frame.MessageID,
			Text:      frame.Text,
		}); err != nil {
			d.logger.Warn("tg editMessageText failed", "instance", shortID(instanceID), "err", err)
		}
	case "download":
		d.logger.Info("claude->tg download",
			"instance", shortID(instanceID), "file_id", frame.FileID, "req_id", frame.ReqID,
		)
		path, err := d.tg.DownloadFile(ctx, frame.FileID, d.opts.AttachDir)
		resp := ws.Frame{Type: "download_result", ReqID: frame.ReqID, Path: path}
		if err != nil {
			resp.Err = err.Error()
			d.logger.Warn("tg downloadFile failed", "instance", shortID(instanceID), "err", err)
		} else {
			d.logger.Info("tg downloadFile ok", "instance", shortID(instanceID), "path", path)
		}
		d.sendTo(instanceID, resp)
	case "tts":
		d.logger.Info("claude->tg tts",
			"instance", shortID(instanceID), "text_len", len(frame.Text),
		)
		inst, _ := d.store.Get(instanceID)
		if inst == nil {
			d.logger.Warn("tts for unknown instance", "instance", instanceID)
			d.sendTo(instanceID, ws.Frame{Type: "tts_result", ReqID: frame.ReqID, Err: "unknown instance"})
			return
		}
		if !d.media.CanSynthesize() {
			errMsg := "TTS not configured. Set TRD_TTS_CMD (e.g. kokoro) or TRD_OPENAI_API_KEY."
			d.logger.Warn("tts not configured", "instance", shortID(instanceID))
			d.sendTo(instanceID, ws.Frame{Type: "tts_result", ReqID: frame.ReqID, Err: errMsg})
			return
		}
		audioPath, err := d.media.Synthesize(ctx, frame.Text, d.opts.AttachDir)
		if err != nil {
			d.logger.Warn("tts synthesis failed", "instance", shortID(instanceID), "err", err)
			d.sendTo(instanceID, ws.Frame{Type: "tts_result", ReqID: frame.ReqID, Err: err.Error()})
			return
		}
		if _, err := d.tg.SendVoice(ctx, inst.ChatID, inst.TopicID, audioPath, ""); err != nil {
			d.logger.Warn("tg sendVoice failed", "instance", shortID(instanceID), "err", err)
			d.sendTo(instanceID, ws.Frame{Type: "tts_result", ReqID: frame.ReqID, Err: err.Error()})
		} else {
			d.logger.Info("tg sendVoice ok", "instance", shortID(instanceID), "path", audioPath)
			d.sendTo(instanceID, ws.Frame{Type: "tts_result", ReqID: frame.ReqID, Path: audioPath})
		}
	case "hello":
		d.logger.Info("channel hello", "instance", shortID(instanceID), "claims", shortID(frame.InstanceID))
	default:
		d.logger.Warn("unknown frame type", "instance", shortID(instanceID), "type", frame.Type)
	}
}

// sendTo pushes a frame to the given instance's WS writer channel. Drops if no conn.
func (d *Dispatcher) sendTo(instanceID string, frame ws.Frame) {
	d.mu.RLock()
	live, ok := d.conns[instanceID]
	d.mu.RUnlock()
	if !ok {
		d.logger.Warn("no live channel — dropping frame (claude session not connected?)",
			"instance", shortID(instanceID), "frame_type", frame.Type,
		)
		return
	}
	select {
	case live.inbound <- frame:
		d.logger.Debug("frame queued to channel",
			"instance", shortID(instanceID), "frame_type", frame.Type,
			"queue_depth", len(live.inbound),
		)
	default:
		d.logger.Warn("inbound channel full — dropping frame",
			"instance", shortID(instanceID), "frame_type", frame.Type,
		)
	}
}

// ListInstances returns all instances as JSON for the CLI API endpoint,
// enriched with runtime WS connection and tmux liveness state.
func (d *Dispatcher) ListInstances() ([]byte, error) {
	all, err := d.store.All()
	if err != nil {
		return nil, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	infos := make([]InstanceInfo, len(all))
	for i, inst := range all {
		_, connected := d.conns[inst.InstanceID]
		infos[i] = InstanceInfo{
			Instance:  inst,
			Connected: connected,
			TmuxAlive: tmuxmgr.HasSession(tmuxmgr.SessionName(inst.InstanceID)),
		}
	}
	return json.Marshal(infos)
}

// isUserAllowed checks a Telegram username against the combined allowlist
// (stored users + TRD_ALLOWED_USERNAMES env var). An empty combined list
// means everyone is allowed (backwards compatible).
func (d *Dispatcher) isUserAllowed(username string) bool {
	if username == "" {
		// No username to check — allow (Telegram users without usernames
		// can't be allowlisted, so blocking them would be surprising).
		return true
	}
	username = strings.ToLower(username)

	// Check env var first.
	if env := os.Getenv("TRD_ALLOWED_USERNAMES"); env != "" {
		for _, u := range strings.Split(env, ",") {
			if strings.ToLower(strings.TrimSpace(u)) == username {
				return true
			}
		}
		// Env is set — also check stored list before rejecting.
		if d.store.IsAllowedUser(username) {
			return true
		}
		return false
	}

	// No env var — check stored list. Empty list = allow all.
	stored, _ := d.store.ListAllowedUsers()
	if len(stored) == 0 {
		return true
	}
	return d.store.IsAllowedUser(username)
}

// AllowedUsers returns the stored allowlist.
func (d *Dispatcher) AllowedUsers() ([]string, error) { return d.store.ListAllowedUsers() }

// AddAllowedUser adds a username to the stored allowlist.
func (d *Dispatcher) AddAllowedUser(username string) error { return d.store.AddAllowedUser(username) }

// RemoveAllowedUser removes a username from the stored allowlist.
func (d *Dispatcher) RemoveAllowedUser(username string) error {
	return d.store.RemoveAllowedUser(username)
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
		updates, raws, err := d.tg.GetUpdatesRaw(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.logger.Warn("getUpdates failed", "err", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for i, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			// Log the exact payload Telegram delivered, so we can tell the
			// difference between "bot never got it" (privacy mode) and "we
			// parsed it but ignored it" (wrong update type / filter).
			d.logger.Info("tg raw update", "update_id", u.UpdateID, "raw", string(raws[i]))
			if u.Message != nil {
				d.handleMessage(ctx, u.Message)
			}
			if u.EditedMessage != nil {
				d.handleEditedMessage(ctx, u.EditedMessage)
			}
		}
	}
}

func (d *Dispatcher) handleMessage(ctx context.Context, m *telegram.Message) {
	user := ""
	if m.From != nil {
		user = m.From.Username
		if user == "" {
			user = m.From.FirstName
		}
	}
	rawText := m.Text
	if rawText == "" {
		rawText = m.Caption
	}
	hasDoc := m.Document != nil
	nPhotos := len(m.Photo)
	hasVoice := m.Voice != nil
	hasAudio := m.Audio != nil
	d.logger.Info("tg recv",
		"chat", m.Chat.ID, "chat_type", m.Chat.Type, "is_forum", m.Chat.IsForum,
		"thread", m.MessageThreadID, "msg_id", m.MessageID,
		"from", user, "text_len", len(rawText), "text", preview(rawText),
		"has_doc", hasDoc, "photos", nPhotos, "voice", hasVoice, "audio", hasAudio,
	)
	if m.Chat.Type != "supergroup" || !m.Chat.IsForum {
		d.logger.Info("tg recv rejected: not forum supergroup", "chat", m.Chat.ID, "chat_type", m.Chat.Type)
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "TRD requires a forum supergroup (topics enabled). This chat is "+m.Chat.Type+".")
		return
	}
	if !d.isUserAllowed(user) {
		d.logger.Info("tg recv rejected: user not in allowlist", "user", user)
		return
	}
	text := strings.TrimSpace(rawText)

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
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "Usage: /start <git-url>")
	case text == "/stop":
		d.cmdStop(ctx, m)
	case text == "/restart":
		d.cmdRestart(ctx, m)
	case text == "/status":
		d.cmdStatus(ctx, m)
	case text == "/forget":
		d.cmdForget(ctx, m)
	case text == "/watch":
		d.cmdWatch(ctx, m)
	default:
		d.routeToInstance(ctx, m, text)
	}
}

// --- commands ---

func (d *Dispatcher) cmdStart(ctx context.Context, m *telegram.Message, repoURL string) {
	if repoURL == "" {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "Usage: /start <git-url>\nAccepted formats:\n  git@github.com:org/repo.git\n  https://github.com/org/repo\n  github.com/org/repo")
		return
	}
	normalized, err := normalizeRepoURL(repoURL)
	if err != nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "Invalid repo URL: "+err.Error())
		return
	}
	repoURL = normalized
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
	claudeBin := firstNonEmpty(os.Getenv("TRD_CLAUDE_BIN"), "claude")
	if _, err := exec.LookPath(claudeBin); err != nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID,
			fmt.Sprintf("%q not found on PATH. Install Claude Code first.", claudeBin))
		return
	}

	reposDir, _ := config.ReposDir()
	repoPath := filepath.Join(reposDir, instID)

	sent, _ := d.tg.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:          m.Chat.ID,
		MessageThreadID: m.MessageThreadID,
		Text:            "Cloning " + repoURL + "…",
	})

	cloneCtx, cloneCancel := context.WithTimeout(ctx, 5*time.Minute)
	cloneDone := make(chan struct{})
	var cloneOut []byte
	var cloneErr error
	go func() {
		cloneOut, cloneErr = exec.CommandContext(cloneCtx, "git", "clone", repoURL, repoPath).CombinedOutput()
		close(cloneDone)
	}()

	// Update progress message every 10s while clone runs.
	if sent.MessageID != 0 {
		start := time.Now()
		ticker := time.NewTicker(10 * time.Second)
	loop:
		for {
			select {
			case <-cloneDone:
				break loop
			case <-ticker.C:
				elapsed := time.Since(start).Truncate(time.Second)
				_ = d.tg.EditMessageText(ctx, telegram.EditMessageTextParams{
					ChatID:    m.Chat.ID,
					MessageID: sent.MessageID,
					Text:      fmt.Sprintf("Cloning %s… (%s elapsed)", repoURL, elapsed),
				})
			}
		}
		ticker.Stop()
	} else {
		<-cloneDone
	}
	cloneCancel()

	if cloneErr != nil {
		_ = os.RemoveAll(repoPath)
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "clone failed:\n"+truncate(string(cloneOut), 1500))
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
		RepoName:   storage.RepoNameFromURL(repoURL),
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

func (d *Dispatcher) cmdWatch(ctx context.Context, m *telegram.Message) {
	inst, _ := d.store.ByTopic(m.Chat.ID, m.MessageThreadID)
	if inst == nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "no instance bound to this topic")
		return
	}
	out, err := tmuxmgr.CapturePane(tmuxmgr.SessionName(inst.InstanceID))
	if err != nil {
		d.sendText(ctx, m.Chat.ID, m.MessageThreadID, "capture failed: "+err.Error())
		return
	}
	out = strings.TrimSpace(out)
	if out == "" {
		out = "(empty pane)"
	}
	d.sendText(ctx, m.Chat.ID, m.MessageThreadID, truncate(out, 4000))
}

// routeToInstance forwards a non-command message to the bound instance's channel plugin.
func (d *Dispatcher) routeToInstance(ctx context.Context, m *telegram.Message, text string) {
	inst, err := d.store.ByTopic(m.Chat.ID, m.MessageThreadID)
	if err != nil {
		d.logger.Warn("route: ByTopic lookup failed",
			"chat", m.Chat.ID, "thread", m.MessageThreadID, "err", err)
		return
	}
	if inst == nil {
		// Not bound — silently ignore so the bot doesn't spam every chat it's in.
		d.logger.Debug("route: no instance bound to topic — ignoring",
			"chat", m.Chat.ID, "thread", m.MessageThreadID)
		return
	}
	if inst.State != storage.StateRunning {
		d.logger.Info("route: instance not running",
			"instance", shortID(inst.InstanceID), "state", inst.State)
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
		ph := m.Photo[len(m.Photo)-1]
		frame.AttachmentFileID = ph.FileID
	} else if m.Voice != nil {
		frame.AttachmentFileID = m.Voice.FileID
		frame.AttachmentName = "voice.ogg"
		if d.media.CanTranscribe() {
			if transcript := d.transcribeAttachment(ctx, m.Voice.FileID); transcript != "" {
				frame.Text = transcript
			}
		}
	} else if m.Audio != nil {
		frame.AttachmentFileID = m.Audio.FileID
		frame.AttachmentName = m.Audio.FileName
		if d.media.CanTranscribe() {
			if transcript := d.transcribeAttachment(ctx, m.Audio.FileID); transcript != "" {
				frame.Text = transcript
			}
		}
	}
	d.mu.RLock()
	_, connected := d.conns[inst.InstanceID]
	d.mu.RUnlock()
	d.logger.Info("tg->claude forward",
		"instance", shortID(inst.InstanceID),
		"chat", frame.ChatID, "thread", frame.ThreadID, "msg_id", frame.MessageID,
		"from", user, "text_len", len(text), "text", preview(text),
		"attach", frame.AttachmentFileID != "",
		"channel_connected", connected,
	)
	d.sendTo(inst.InstanceID, frame)
}

// handleEditedMessage forwards an edited Telegram message to the bound instance
// so the user's corrections aren't silently lost.
func (d *Dispatcher) handleEditedMessage(_ context.Context, m *telegram.Message) {
	if m.Chat.Type != "supergroup" || !m.Chat.IsForum {
		return
	}
	user := ""
	if m.From != nil {
		user = m.From.Username
		if user == "" {
			user = m.From.FirstName
		}
	}
	if !d.isUserAllowed(user) {
		return
	}
	inst, _ := d.store.ByTopic(m.Chat.ID, m.MessageThreadID)
	if inst == nil || inst.State != storage.StateRunning {
		return
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	d.logger.Info("tg edited->claude",
		"instance", shortID(inst.InstanceID), "msg_id", m.MessageID, "from", user,
	)
	frame := ws.Frame{
		Type:      "message",
		ChatID:    m.Chat.ID,
		MessageID: m.MessageID,
		ThreadID:  m.MessageThreadID,
		User:      user,
		Text:      fmt.Sprintf("(edited) %s", text),
		TS:        m.Date,
	}
	d.sendTo(inst.InstanceID, frame)
}

// sendFileSmartly picks the best Telegram send method based on file extension.
func (d *Dispatcher) sendFileSmartly(ctx context.Context, chatID int64, threadID int, path, instanceID string) error {
	ext := strings.ToLower(filepath.Ext(path))
	var err error
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		_, err = d.tg.SendPhoto(ctx, chatID, threadID, path, "")
		if err == nil {
			d.logger.Info("tg sendPhoto ok", "instance", shortID(instanceID), "path", path)
		}
	case ".ogg", ".oga", ".opus":
		_, err = d.tg.SendVoice(ctx, chatID, threadID, path, "")
		if err == nil {
			d.logger.Info("tg sendVoice ok", "instance", shortID(instanceID), "path", path)
		}
	case ".mp3", ".m4a", ".wav", ".flac", ".aac":
		_, err = d.tg.SendAudio(ctx, chatID, threadID, path, "")
		if err == nil {
			d.logger.Info("tg sendAudio ok", "instance", shortID(instanceID), "path", path)
		}
	default:
		_, err = d.tg.SendDocument(ctx, chatID, threadID, path, "")
		if err == nil {
			d.logger.Info("tg sendDocument ok", "instance", shortID(instanceID), "path", path)
		}
	}
	return err
}

// transcribeAttachment downloads a Telegram file and runs Whisper on it.
// Returns the transcript, or empty string on any failure (logged but not fatal).
// Cleans up sidecar files (.txt, .vtt, .srt, .json) that whisper CLI may create.
func (d *Dispatcher) transcribeAttachment(ctx context.Context, fileID string) string {
	path, err := d.tg.DownloadFile(ctx, fileID, d.opts.AttachDir)
	if err != nil {
		d.logger.Warn("whisper: download failed", "file_id", fileID, "err", err)
		return ""
	}
	transcript, err := d.media.Transcribe(ctx, path)
	if err != nil {
		d.logger.Warn("whisper: transcription failed", "path", path, "err", err)
		return ""
	}
	// Whisper CLI writes sidecar files (e.g. file.txt alongside file.ogg). Clean them up.
	base := strings.TrimSuffix(path, filepath.Ext(path))
	for _, ext := range []string{".txt", ".vtt", ".srt", ".json", ".tsv"} {
		sidecar := base + ext
		if err := os.Remove(sidecar); err == nil {
			d.logger.Debug("whisper: cleaned up sidecar", "path", sidecar)
		}
	}
	d.logger.Info("whisper: transcribed", "path", path, "len", len(transcript))
	return transcript
}

// --- internals ---

func (d *Dispatcher) launchTmux(inst storage.Instance) error {
	name := tmuxmgr.SessionName(inst.InstanceID)
	if tmuxmgr.HasSession(name) {
		d.logger.Info("launchTmux: session already exists", "instance", shortID(inst.InstanceID), "session", name)
		return nil
	}
	cfgPath := filepath.Join(inst.RepoPath, ".trd", "config.json")
	env := []string{
		"TRD_CONFIG=" + cfgPath,
		"TRD_INSTANCE_ID=" + inst.InstanceID,
	}

	claudeBin := firstNonEmpty(os.Getenv("TRD_CLAUDE_BIN"), "claude")
	claudeArgs := firstNonEmpty(os.Getenv("TRD_CLAUDE_ARGS"),
		"--debug --dangerously-skip-permissions --dangerously-load-development-channels server:trd-channel")

	cmd := fmt.Sprintf("%s %s", claudeBin, claudeArgs)
	d.logger.Info("launchTmux",
		"instance", shortID(inst.InstanceID), "session", name, "cwd", inst.RepoPath,
	)
	if err := tmuxmgr.NewSession(name, inst.RepoPath, cmd, env); err != nil {
		return err
	}

	// Auto-confirm the dev-channels prompt by detecting it in the pane
	// instead of the old fragile sleep+send-keys approach.
	keys := firstNonEmpty(os.Getenv("TRD_CLAUDE_CONFIRM_KEYS"), "Enter")
	if keys != "" {
		go d.autoConfirm(name, keys, inst.InstanceID)
	}
	return nil
}

// autoConfirm polls the tmux pane looking for a confirmation prompt and
// sends keystrokes when it detects one. This replaces the old fragile
// "sleep N; tmux send-keys Enter" shell workaround.
func (d *Dispatcher) autoConfirm(sessionName, keys, instanceID string) {
	const timeout = 30 * time.Second
	const interval = 500 * time.Millisecond
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		time.Sleep(interval)
		if !tmuxmgr.HasSession(sessionName) {
			return
		}
		out, err := tmuxmgr.CapturePane(sessionName)
		if err != nil {
			continue
		}
		out = strings.TrimSpace(out)
		if out == "" {
			continue
		}
		// Look for a confirmation prompt on the last non-empty line.
		lines := strings.Split(out, "\n")
		last := strings.TrimSpace(lines[len(lines)-1])
		if strings.Contains(last, "?") || strings.Contains(strings.ToLower(last), "y/n") {
			d.logger.Info("autoConfirm: detected prompt, sending keys",
				"instance", shortID(instanceID), "session", sessionName, "prompt", preview(last))
			_ = tmuxmgr.SendKeys(sessionName, keys)
			return
		}
	}
	d.logger.Warn("autoConfirm: timed out without detecting prompt",
		"instance", shortID(instanceID), "session", sessionName)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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

func (d *Dispatcher) sweepAttachments() {
	maxAge := 7 * 24 * time.Hour
	entries, err := os.ReadDir(d.opts.AttachDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(d.opts.AttachDir, e.Name())
			if err := os.Remove(path); err == nil {
				d.logger.Info("swept old attachment", "path", path, "age_days", int(time.Since(info.ModTime()).Hours()/24))
			}
		}
	}
}

func (d *Dispatcher) checkHealth(ctx context.Context) {
	d.sweepAttachments()
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

// normalizeRepoURL accepts three URL formats and converts to SSH:
//   git@host:org/repo.git   → pass through
//   https://host/org/repo   → git@host:org/repo.git
//   host/org/repo           → git@host:org/repo.git
// Rejects flag-like input (starts with -) and URLs that don't look like a valid repo path.
func normalizeRepoURL(raw string) (string, error) {
	if strings.HasPrefix(raw, "-") {
		return "", fmt.Errorf("URL must not start with a dash")
	}

	// SSH format: git@host:path
	if strings.HasPrefix(raw, "git@") {
		// Minimal validation: must have a colon and path after it.
		if !strings.Contains(raw[4:], ":") {
			return "", fmt.Errorf("SSH URL missing colon: %q", raw)
		}
		if !strings.HasSuffix(raw, ".git") {
			raw += ".git"
		}
		return raw, nil
	}

	// Strip scheme if present.
	u := raw
	if after, ok := strings.CutPrefix(u, "https://"); ok {
		u = after
	} else if after, ok := strings.CutPrefix(u, "http://"); ok {
		u = after
	}

	// Now we expect: host/org/repo or host/org/repo.git
	// Must have at least host/org/repo (2 slashes worth of path).
	parts := strings.SplitN(u, "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", fmt.Errorf("expected format: host/org/repo, got %q", raw)
	}
	host := parts[0]
	path := parts[1] + "/" + parts[2]
	path = strings.TrimSuffix(path, ".git")

	return fmt.Sprintf("git@%s:%s.git", host, path), nil
}

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

// shortID returns the first 8 chars of an instance ID for compact log output.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// preview returns a single-line, length-capped sample of s suitable for logs.
func preview(s string) string {
	const max = 200
	// Collapse newlines so log records stay on one line.
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// writeMCPConfig merges a trd-channel entry into the repo's .mcp.json so Claude
// finds the channel plugin. If the repo already has an .mcp.json, we preserve
// existing servers and only add/overwrite the "trd-channel" key.
//
// Resolution order for the channel command:
//  1. $TRD_CHANNEL_ENTRY set → `bun run <path>` (dev install)
//  2. default               → `trd-channel` (npm bin on PATH)
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

	type serverDef struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	type mcpFile struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}

	var existing mcpFile
	if data, err := os.ReadFile(mcpPath); err == nil {
		_ = json.Unmarshal(data, &existing)
	}
	if existing.MCPServers == nil {
		existing.MCPServers = make(map[string]json.RawMessage)
	}

	trdEntry, _ := json.Marshal(serverDef{Command: command, Args: args})
	existing.MCPServers["trd-channel"] = trdEntry

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(mcpPath, out, 0o644)
}
