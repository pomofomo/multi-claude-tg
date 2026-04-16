// Package media provides optional Whisper transcription and TTS synthesis.
// Both are configured via environment variables and gracefully degrade
// (returning ErrNotConfigured) when the required tools aren't available.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var ErrNotConfigured = errors.New("not configured")

// Config holds media processing settings, all optional.
type Config struct {
	// WhisperCmd is a CLI command that receives an audio file path as its
	// last argument and prints the transcript to stdout. Audio is
	// automatically converted to 16kHz WAV via ffmpeg before invocation.
	// Example: "whisper-cpp -m /path/to/ggml-base.bin --no-prints --no-timestamps -f"
	WhisperCmd string

	// TTSCmd is a CLI command for TTS synthesis.
	// Example: "kokoro" → invoked as: kokoro -i <text.txt> -o <out.ogg>
	TTSCmd string

	// OpenAIAPIKey enables the OpenAI API for Whisper and/or TTS when
	// the corresponding CLI command is not set.
	OpenAIAPIKey string

	// TTSVoice is the OpenAI TTS voice (default: "alloy").
	TTSVoice string

	// TTSModel is the OpenAI TTS model (default: "tts-1").
	TTSModel string
}

// ConfigFromEnv reads media config from environment variables.
func ConfigFromEnv() Config {
	return Config{
		WhisperCmd:   os.Getenv("TRD_WHISPER_CMD"),
		TTSCmd:       os.Getenv("TRD_TTS_CMD"),
		OpenAIAPIKey: os.Getenv("TRD_OPENAI_API_KEY"),
		TTSVoice:     envOrDefault("TRD_TTS_VOICE", "alloy"),
		TTSModel:     envOrDefault("TRD_TTS_MODEL", "tts-1"),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// CanTranscribe reports whether Whisper transcription is available.
func (c Config) CanTranscribe() bool {
	return c.WhisperCmd != "" || c.OpenAIAPIKey != ""
}

// CanSynthesize reports whether TTS synthesis is available.
func (c Config) CanSynthesize() bool {
	return c.TTSCmd != "" || c.OpenAIAPIKey != ""
}

// Transcribe converts an audio file to text.
// Returns ErrNotConfigured if no Whisper backend is available.
func (c Config) Transcribe(ctx context.Context, audioPath string) (string, error) {
	if c.WhisperCmd != "" {
		return c.transcribeCLI(ctx, audioPath)
	}
	if c.OpenAIAPIKey != "" {
		return c.transcribeOpenAI(ctx, audioPath)
	}
	return "", ErrNotConfigured
}

func (c Config) transcribeCLI(ctx context.Context, audioPath string) (string, error) {
	// whisper-cpp requires WAV input. Convert via ffmpeg if the file isn't already WAV.
	inputPath := audioPath
	if ext := strings.ToLower(filepath.Ext(audioPath)); ext != ".wav" {
		wavPath := audioPath + ".wav"
		ffmpeg := exec.CommandContext(ctx, "ffmpeg", "-i", audioPath, "-ar", "16000", "-ac", "1", wavPath, "-y")
		if ffOut, err := ffmpeg.CombinedOutput(); err != nil {
			return "", fmt.Errorf("ffmpeg convert failed: %w\noutput: %s", err, ffOut)
		}
		inputPath = wavPath
		defer os.Remove(wavPath)
	}

	parts := strings.Fields(c.WhisperCmd)
	args := append(parts[1:], inputPath)
	cmd := exec.CommandContext(ctx, parts[0], args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("whisper cmd failed: %w\nstderr: %s", err, exitErr.Stderr)
		}
		return "", fmt.Errorf("whisper cmd: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (c Config) transcribeOpenAI(ctx context.Context, audioPath string) (string, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "whisper-1")
	fw, err := mw.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		"https://api.openai.com/v1/audio/transcriptions", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.OpenAIAPIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("openai whisper: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Text), nil
}

// Synthesize converts text to an audio file (OGG preferred).
// Returns the path to the generated audio file.
// Returns ErrNotConfigured if no TTS backend is available.
func (c Config) Synthesize(ctx context.Context, text, outDir string) (string, error) {
	if c.TTSCmd != "" {
		return c.synthesizeCLI(ctx, text, outDir)
	}
	if c.OpenAIAPIKey != "" {
		return c.synthesizeOpenAI(ctx, text, outDir)
	}
	return "", ErrNotConfigured
}

func (c Config) synthesizeCLI(ctx context.Context, text, outDir string) (string, error) {
	textFile, err := os.CreateTemp(outDir, "tts-in-*.txt")
	if err != nil {
		return "", err
	}
	if _, err := textFile.WriteString(text); err != nil {
		textFile.Close()
		return "", err
	}
	textFile.Close()
	defer os.Remove(textFile.Name())

	outPath := filepath.Join(outDir, fmt.Sprintf("tts-%d.ogg", time.Now().UnixNano()))
	parts := strings.Fields(c.TTSCmd)
	args := append(parts[1:], "-i", textFile.Name(), "-o", outPath)
	cmd := exec.CommandContext(ctx, parts[0], args...)
	cmdOut, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tts cmd failed: %w\noutput: %s", err, cmdOut)
	}
	if _, err := os.Stat(outPath); err != nil {
		return "", fmt.Errorf("tts cmd did not produce output at %s", outPath)
	}
	return outPath, nil
}

func (c Config) synthesizeOpenAI(ctx context.Context, text, outDir string) (string, error) {
	voice := c.TTSVoice
	model := c.TTSModel

	body, _ := json.Marshal(map[string]string{
		"model":           model,
		"input":           text,
		"voice":           voice,
		"response_format": "opus",
	})

	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		"https://api.openai.com/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.OpenAIAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("openai tts: HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	outPath := filepath.Join(outDir, fmt.Sprintf("tts-%d.ogg", time.Now().UnixNano()))
	out, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", err
	}
	return outPath, nil
}
