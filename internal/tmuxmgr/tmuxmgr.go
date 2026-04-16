// Package tmuxmgr wraps the subset of tmux commands TRD needs.
package tmuxmgr

import (
	"fmt"
	"os/exec"
	"strings"
)

// SessionName returns the tmux session name for an instance.
func SessionName(instanceID string) string {
	return "trd-" + instanceID
}

// HasSession returns true if tmux has a session with the given name.
func HasSession(name string) bool {
	err := exec.Command("tmux", "has-session", "-t", name).Run()
	return err == nil
}

// NewSession creates a detached session running cmd in workdir with env vars.
// env entries are "KEY=VALUE" strings.
func NewSession(name, workdir, cmd string, env []string) error {
	args := []string{"new-session", "-d", "-s", name, "-c", workdir}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	args = append(args, cmd)
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux new-session failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// KillSession ends a session. No error if it's already gone.
func KillSession(name string) error {
	if !HasSession(name) {
		return nil
	}
	out, err := exec.Command("tmux", "kill-session", "-t", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux kill-session failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CapturePane returns the current visible content of the session's active pane.
func CapturePane(name string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", name).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SendKeys sends keystrokes to a tmux session's active pane.
func SendKeys(name string, keys ...string) error {
	args := append([]string{"send-keys", "-t", name}, keys...)
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
