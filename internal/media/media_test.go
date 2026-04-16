package media

import (
	"context"
	"errors"
	"testing"
)

func TestConfigFromEnvDefaults(t *testing.T) {
	cfg := Config{}
	if cfg.CanTranscribe() {
		t.Error("empty config should not be able to transcribe")
	}
	if cfg.CanSynthesize() {
		t.Error("empty config should not be able to synthesize")
	}
}

func TestCanTranscribeWithWhisperCmd(t *testing.T) {
	cfg := Config{WhisperCmd: "whisper"}
	if !cfg.CanTranscribe() {
		t.Error("should be able to transcribe with WhisperCmd set")
	}
}

func TestCanTranscribeWithOpenAI(t *testing.T) {
	cfg := Config{OpenAIAPIKey: "sk-test"}
	if !cfg.CanTranscribe() {
		t.Error("should be able to transcribe with OpenAIAPIKey set")
	}
}

func TestCanSynthesizeWithTTSCmd(t *testing.T) {
	cfg := Config{TTSCmd: "kokoro"}
	if !cfg.CanSynthesize() {
		t.Error("should be able to synthesize with TTSCmd set")
	}
}

func TestCanSynthesizeWithOpenAI(t *testing.T) {
	cfg := Config{OpenAIAPIKey: "sk-test"}
	if !cfg.CanSynthesize() {
		t.Error("should be able to synthesize with OpenAIAPIKey set")
	}
}

func TestTranscribeNotConfigured(t *testing.T) {
	cfg := Config{}
	_, err := cfg.Transcribe(context.Background(), "/nonexistent")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}

func TestSynthesizeNotConfigured(t *testing.T) {
	cfg := Config{}
	_, err := cfg.Synthesize(context.Background(), "hello", t.TempDir())
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}

func TestEnvOrDefault(t *testing.T) {
	if got := envOrDefault("TRD_NONEXISTENT_TEST_VAR_XYZ", "fallback"); got != "fallback" {
		t.Errorf("expected fallback, got %q", got)
	}
}
