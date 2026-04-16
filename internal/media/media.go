// Package media provides optional Whisper transcription and TTS synthesis.
// Whisper transcription uses an external CLI (whisper-cpp). TTS uses
// sherpa-onnx embedded directly in the Go binary, with OpenAI API as fallback.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

var ErrNotConfigured = errors.New("not configured")

// Config holds media processing settings, all optional.
type Config struct {
	// WhisperCmd is a CLI command that receives an audio file path as its
	// last argument and prints the transcript to stdout. Audio is
	// automatically converted to 16kHz WAV via ffmpeg before invocation.
	// Example: "whisper-cpp -m /path/to/ggml-base.bin --no-prints --no-timestamps -f"
	WhisperCmd string

	// TTSModelDir is the path to a sherpa-onnx VITS piper model directory.
	// Must contain: <name>.onnx, tokens.txt, espeak-ng-data/
	// Example: "~/.local/share/sherpa-onnx-tts/vits-piper-en_US-lessac-medium"
	TTSModelDir string

	// OpenAIAPIKey enables the OpenAI API for Whisper and/or TTS when
	// the primary backend is not configured.
	OpenAIAPIKey string
}

// ConfigFromEnv reads media config from environment variables.
func ConfigFromEnv() Config {
	return Config{
		WhisperCmd:   os.Getenv("TRD_WHISPER_CMD"),
		TTSModelDir:  os.Getenv("TRD_TTS_MODEL_DIR"),
		OpenAIAPIKey: os.Getenv("TRD_OPENAI_API_KEY"),
	}
}

// Engine wraps Config and holds a persistent sherpa-onnx TTS instance.
// Create with NewEngine; call Close when done.
type Engine struct {
	Config

	mu  sync.Mutex
	tts *sherpa.OfflineTts
}

// NewEngine creates a media engine. If TTSModelDir is set, initializes
// the sherpa-onnx TTS engine (loads model into memory once).
func NewEngine(cfg Config) (*Engine, error) {
	e := &Engine{Config: cfg}
	if cfg.TTSModelDir != "" {
		if err := e.initTTS(); err != nil {
			return nil, fmt.Errorf("init TTS: %w", err)
		}
	}
	return e, nil
}

func (e *Engine) initTTS() error {
	dir := e.TTSModelDir

	// Find the .onnx file in the model directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read model dir %s: %w", dir, err)
	}
	var onnxFile string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".onnx") && !strings.HasSuffix(entry.Name(), ".onnx.json") {
			onnxFile = filepath.Join(dir, entry.Name())
			break
		}
	}
	if onnxFile == "" {
		return fmt.Errorf("no .onnx file found in %s", dir)
	}

	tokensFile := filepath.Join(dir, "tokens.txt")
	dataDir := filepath.Join(dir, "espeak-ng-data")

	config := sherpa.OfflineTtsConfig{
		Model: sherpa.OfflineTtsModelConfig{
			Vits: sherpa.OfflineTtsVitsModelConfig{
				Model:       onnxFile,
				Tokens:      tokensFile,
				DataDir:     dataDir,
				NoiseScale:  0.667,
				NoiseScaleW: 0.8,
				LengthScale: 1.0,
			},
			NumThreads: 2,
			Debug:      0,
			Provider:   "cpu",
		},
		MaxNumSentences: 2,
	}

	e.tts = sherpa.NewOfflineTts(&config)
	if e.tts == nil {
		return fmt.Errorf("sherpa-onnx NewOfflineTts returned nil")
	}
	return nil
}

// Close releases the sherpa-onnx TTS resources.
func (e *Engine) Close() {
	if e.tts != nil {
		sherpa.DeleteOfflineTts(e.tts)
		e.tts = nil
	}
}

// CanTranscribe reports whether Whisper transcription is available.
func (e *Engine) CanTranscribe() bool {
	return e.WhisperCmd != "" || e.OpenAIAPIKey != ""
}

// CanSynthesize reports whether TTS synthesis is available.
func (e *Engine) CanSynthesize() bool {
	return e.tts != nil || e.OpenAIAPIKey != ""
}

// Transcribe converts an audio file to text.
func (e *Engine) Transcribe(ctx context.Context, audioPath string) (string, error) {
	if e.WhisperCmd != "" {
		return e.transcribeCLI(ctx, audioPath)
	}
	if e.OpenAIAPIKey != "" {
		return e.transcribeOpenAI(ctx, audioPath)
	}
	return "", ErrNotConfigured
}

// Synthesize converts text to an OGG audio file.
// Returns the path to the generated audio file.
func (e *Engine) Synthesize(ctx context.Context, text, outDir string) (string, error) {
	if e.tts != nil {
		return e.synthesizeSherpa(ctx, text, outDir)
	}
	if e.OpenAIAPIKey != "" {
		return e.synthesizeOpenAI(ctx, text, outDir)
	}
	return "", ErrNotConfigured
}

// --- Whisper transcription ---

func (e *Engine) transcribeCLI(ctx context.Context, audioPath string) (string, error) {
	// whisper-cpp requires WAV input. Convert via ffmpeg if not already WAV.
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

	parts := strings.Fields(e.WhisperCmd)
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

func (e *Engine) transcribeOpenAI(ctx context.Context, audioPath string) (string, error) {
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
	req.Header.Set("Authorization", "Bearer "+e.OpenAIAPIKey)
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

// --- TTS synthesis ---

func (e *Engine) synthesizeSherpa(_ context.Context, text, outDir string) (string, error) {
	// sherpa-onnx is not thread-safe for concurrent Generate calls.
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := sherpa.GenerationConfig{
		SilenceScale: 0.2,
		Speed:        float32(math.Max(1.0, 1e-6)),
		Sid:          0,
	}

	audio := e.tts.GenerateWithConfig(text, &cfg, nil)
	if len(audio.Samples) == 0 {
		return "", fmt.Errorf("sherpa-onnx produced no audio for text: %s", text[:min(len(text), 50)])
	}

	// Save as WAV first.
	wavPath := filepath.Join(outDir, fmt.Sprintf("tts-%d.wav", time.Now().UnixNano()))
	if ok := audio.Save(wavPath); !ok {
		return "", fmt.Errorf("sherpa-onnx failed to save WAV to %s", wavPath)
	}
	defer os.Remove(wavPath)

	// Convert WAV to OGG/Opus for Telegram voice messages.
	oggPath := filepath.Join(outDir, fmt.Sprintf("tts-%d.ogg", time.Now().UnixNano()))
	ffmpeg := exec.Command("ffmpeg", "-i", wavPath, "-c:a", "libopus", "-b:a", "64k", oggPath, "-y")
	if ffOut, err := ffmpeg.CombinedOutput(); err != nil {
		// If ffmpeg fails, fall back to sending the WAV.
		_ = os.Rename(wavPath, oggPath)
		_ = ffOut
	}
	return oggPath, nil
}

func (e *Engine) synthesizeOpenAI(ctx context.Context, text, outDir string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"model":           "tts-1",
		"input":           text,
		"voice":           "alloy",
		"response_format": "opus",
	})

	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		"https://api.openai.com/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+e.OpenAIAPIKey)
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
