// Command trd is the Telegram Repo Dispatcher binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/pomofomo/multi-claude-tg/internal/config"
	"github.com/pomofomo/multi-claude-tg/internal/dispatcher"
	"github.com/pomofomo/multi-claude-tg/internal/storage"
	"github.com/pomofomo/multi-claude-tg/internal/tmuxmgr"
)

const usage = `trd — Telegram Repo Dispatcher

Usage:
  trd start --telegram-token <token> [--port 7777]
  trd status
  trd stop <topic-or-instance-prefix>
  trd logs <topic-or-instance-prefix>

Env:
  TELEGRAM_BOT_TOKEN     default for --telegram-token
  TRD_PORT               default for --port (7777)
  TRD_HEALTH_INTERVAL_SEC health-loop interval (default 30)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]
	switch sub {
	case "start":
		cmdStart(args)
	case "status":
		cmdStatus(args)
	case "stop":
		cmdStop(args)
	case "logs":
		cmdLogs(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", sub, usage)
		os.Exit(2)
	}
}

func cmdStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	token := fs.String("telegram-token", os.Getenv("TELEGRAM_BOT_TOKEN"), "Telegram bot token")
	port := fs.Int("port", envInt("TRD_PORT", 7777), "dispatcher HTTP/WS port")
	_ = fs.Parse(args)
	if *token == "" {
		fmt.Fprintln(os.Stderr, "--telegram-token is required (or set TELEGRAM_BOT_TOKEN)")
		os.Exit(2)
	}

	logger := newLogger()
	d, err := dispatcher.New(dispatcher.Options{
		TelegramToken:  *token,
		Port:           *port,
		Logger:         logger,
		HealthInterval: time.Duration(envInt("TRD_HEALTH_INTERVAL_SEC", 30)) * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "start failed:", err)
		os.Exit(1)
	}
	defer d.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("trd started", "port", *port)
	if err := d.Run(ctx); err != nil {
		logger.Error("run", "err", err)
		os.Exit(1)
	}
	logger.Info("trd stopped")
}

func cmdStatus(args []string) {
	_ = args
	dbPath, _ := config.StateDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Println("no state db yet — trd has never run")
		return
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open db:", err)
		os.Exit(1)
	}
	defer store.Close()
	all, err := store.All()
	if err != nil {
		fmt.Fprintln(os.Stderr, "list:", err)
		os.Exit(1)
	}
	if len(all) == 0 {
		fmt.Println("no instances")
		return
	}
	for _, inst := range all {
		alive := tmuxmgr.HasSession(tmuxmgr.SessionName(inst.InstanceID))
		fmt.Printf("%s  %s  topic=%d  state=%s  tmux=%v  repo=%s\n",
			inst.InstanceID[:8], shortTime(inst.CreatedAt),
			inst.TopicID, inst.State, alive, inst.RepoURL)
	}
}

func cmdStop(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: trd stop <instance-prefix>")
		os.Exit(2)
	}
	prefix := args[0]
	inst, err := findInstance(prefix)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := tmuxmgr.KillSession(tmuxmgr.SessionName(inst.InstanceID)); err != nil {
		fmt.Fprintln(os.Stderr, "kill:", err)
		os.Exit(1)
	}
	fmt.Println("stopped", inst.InstanceID[:8])
}

func cmdLogs(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: trd logs <instance-prefix>")
		os.Exit(2)
	}
	prefix := args[0]
	inst, err := findInstance(prefix)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := tmuxmgr.CapturePane(tmuxmgr.SessionName(inst.InstanceID))
	if err != nil {
		fmt.Fprintln(os.Stderr, "capture-pane:", err)
		os.Exit(1)
	}
	_, _ = io.Copy(os.Stdout, asReader(out))
}

func findInstance(prefix string) (*storage.Instance, error) {
	dbPath, _ := config.StateDBPath()
	store, err := storage.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	all, err := store.All()
	if err != nil {
		return nil, err
	}
	var matches []storage.Instance
	for _, inst := range all {
		if len(prefix) > 0 && (inst.InstanceID == prefix || startsWith(inst.InstanceID, prefix)) {
			matches = append(matches, inst)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no instance matching %q", prefix)
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("%d instances match %q — use a longer prefix", len(matches), prefix)
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func newLogger() *slog.Logger {
	logPath, _ := config.LogPath()
	_ = os.MkdirAll(dirOf(logPath), 0o700)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	var out io.Writer = os.Stderr
	if err == nil {
		out = io.MultiWriter(os.Stderr, f)
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

func shortTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") }

type stringReader struct{ s string; i int }

func (r *stringReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

func asReader(s string) io.Reader { return &stringReader{s: s} }
