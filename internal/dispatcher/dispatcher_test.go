package dispatcher

import (
	"testing"
)

func TestNormalizeRepoURL(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		// SSH passthrough
		{"git@github.com:org/repo.git", "git@github.com:org/repo.git", false},
		{"git@github.com:org/repo", "git@github.com:org/repo.git", false},
		{"git@gitlab.com:deep/path.git", "git@gitlab.com:deep/path.git", false},

		// HTTPS → SSH
		{"https://github.com/org/repo", "git@github.com:org/repo.git", false},
		{"https://github.com/org/repo.git", "git@github.com:org/repo.git", false},
		{"http://github.com/org/repo", "git@github.com:org/repo.git", false},

		// Bare URL → SSH
		{"github.com/org/repo", "git@github.com:org/repo.git", false},
		{"github.com/org/repo.git", "git@github.com:org/repo.git", false},
		{"gitlab.com/org/repo", "git@gitlab.com:org/repo.git", false},

		// Errors
		{"-flag", "", true},
		{"--option=value", "", true},
		{"noslash", "", true},
		{"host/onlyone", "", true},
		{"", "", true},
		{"git@nocodon", "", true},
	}
	for _, tt := range tests {
		got, err := normalizeRepoURL(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("normalizeRepoURL(%q) = %q, want error", tt.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeRepoURL(%q) error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("normalizeRepoURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
