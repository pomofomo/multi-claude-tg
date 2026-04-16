// Command trd is the Telegram Repo Dispatcher binary.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
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
  trd list
  trd stop  <name-or-prefix>
  trd logs  <name-or-prefix>
  trd shell <name-or-prefix>
  trd cd    <name-or-prefix>

<name-or-prefix> matches against repo name first, then instance ID prefix.

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
	case "status", "list":
		cmdStatus(args)
	case "stop":
		cmdStop(args)
	case "logs":
		cmdLogs(args)
	case "shell":
		cmdShell(args)
	case "cd":
		cmdCd(args)
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

// allInstances tries the running dispatcher's HTTP API first, then falls back
// to opening the bbolt DB directly (which only works when the server is stopped).
func allInstances() ([]storage.Instance, error) {
	port := envInt("TRD_PORT", 7777)
	url := fmt.Sprintf("http://127.0.0.1:%d/api/instances", port)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var instances []storage.Instance
			if err := json.NewDecoder(resp.Body).Decode(&instances); err == nil {
				return instances, nil
			}
		}
	}
	// Fallback: open DB directly (works when server is not running).
	dbPath, _ := config.StateDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil // no DB yet
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer store.Close()
	return store.All()
}

func cmdStatus(args []string) {
	_ = args
	all, err := allInstances()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(all) == 0 {
		fmt.Println("no instances")
		return
	}
	for _, inst := range all {
		alive := tmuxmgr.HasSession(tmuxmgr.SessionName(inst.InstanceID))
		name := inst.RepoName
		if name == "" {
			name = storage.RepoNameFromURL(inst.RepoURL)
		}
		fmt.Printf("%-20s %s  %s  state=%-8s tmux=%v  %s\n",
			name, inst.InstanceID[:8], shortTime(inst.CreatedAt),
			inst.State, alive, inst.RepoURL)
	}
}

func cmdShell(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: trd shell <name-or-prefix>")
		os.Exit(2)
	}
	inst, err := findInstance(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	fmt.Fprintf(os.Stderr, "opening shell in %s (%s)\n", inst.RepoPath, inst.RepoName)
	cmd := exec.Command(shell)
	cmd.Dir = inst.RepoPath
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}

func cmdCd(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: trd cd <name-or-prefix>")
		os.Exit(2)
	}
	inst, err := findInstance(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(inst.RepoPath)
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

func findInstance(query string) (*storage.Instance, error) {
	all, err := allInstances()
	if err != nil {
		return nil, err
	}
	// First pass: match on repo name (exact or prefix).
	var byName []storage.Instance
	for _, inst := range all {
		name := inst.RepoName
		if name == "" {
			name = storage.RepoNameFromURL(inst.RepoURL)
		}
		if strings.EqualFold(name, query) || strings.HasPrefix(strings.ToLower(name), strings.ToLower(query)) {
			byName = append(byName, inst)
		}
	}
	if len(byName) == 1 {
		return &byName[0], nil
	}
	if len(byName) > 1 {
		return nil, fmt.Errorf("%d instances match repo name %q — use a longer prefix or instance ID", len(byName), query)
	}
	// Second pass: match on instance ID prefix.
	var byID []storage.Instance
	for _, inst := range all {
		if strings.HasPrefix(inst.InstanceID, query) {
			byID = append(byID, inst)
		}
	}
	switch len(byID) {
	case 0:
		return nil, fmt.Errorf("no instance matching %q", query)
	case 1:
		return &byID[0], nil
	default:
		return nil, fmt.Errorf("%d instances match %q — use a longer prefix", len(byID), query)
	}
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

func asReader(s string) io.Reader { return strings.NewReader(s) }
