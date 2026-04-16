package media

import (
	"context"
	"errors"
	"testing"
)

func TestEmptyEngineDefaults(t *testing.T) {
	e, err := NewEngine(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.CanTranscribe() {
		t.Error("empty config should not be able to transcribe")
	}
	if e.CanSynthesize() {
		t.Error("empty config should not be able to synthesize")
	}
}

func TestCanTranscribeWithOpenAI(t *testing.T) {
	e, _ := NewEngine(Config{OpenAIAPIKey: "sk-test"})
	defer e.Close()
	if !e.CanTranscribe() {
		t.Error("should be able to transcribe with OpenAIAPIKey set")
	}
}

func TestCanSynthesizeWithOpenAI(t *testing.T) {
	e, _ := NewEngine(Config{OpenAIAPIKey: "sk-test"})
	defer e.Close()
	if !e.CanSynthesize() {
		t.Error("should be able to synthesize with OpenAIAPIKey set")
	}
}

func TestTranscribeNotConfigured(t *testing.T) {
	e, _ := NewEngine(Config{})
	defer e.Close()
	_, err := e.Transcribe(context.Background(), "/nonexistent")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}

func TestSynthesizeNotConfigured(t *testing.T) {
	e, _ := NewEngine(Config{})
	defer e.Close()
	_, err := e.Synthesize(context.Background(), "hello", t.TempDir())
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}
