package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReadRepoConfig(t *testing.T) {
	dir := t.TempDir()
	in := RepoConfig{InstanceID: "i", Secret: "s", DispatcherPort: 7777}
	if err := WriteRepoConfig(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadRepoConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round-trip mismatch: %+v vs %+v", out, in)
	}

	// mode must be 0600
	info, err := os.Stat(filepath.Join(dir, ".trd", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("want perm 0600, got %o", info.Mode().Perm())
	}
}

func TestEnsureGitignoreAppendsOnce(t *testing.T) {
	dir := t.TempDir()
	gi := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gi, []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(gi)
	if strings.Count(string(data), ".trd/") != 1 {
		t.Errorf("want exactly one .trd/ entry, got:\n%s", data)
	}
}

func TestEnsureGitignoreCreatesIfMissing(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ".trd/") {
		t.Errorf("gitignore missing .trd/: %s", data)
	}
}

func TestEnsureGitignoreRecognizesBareTrd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".trd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if strings.Count(string(data), ".trd") != 1 {
		t.Errorf("should not re-append: %s", data)
	}
}
