# Tech Stack

The technologies and libraries used in TRD, explained so you can reuse the same stack for a different project.

## Language: Go 1.23

Go for the main binary. Chosen for single-binary distribution, fast startup, low memory, and excellent concurrency primitives. CGo is required for the audio/ML libraries.

```bash
go.dev/dl   # install
```

## Language: TypeScript (Bun)

The MCP channel plugin is ~470 lines of TypeScript, run with Bun. Bun is used instead of Node because it's faster to start and has native TypeScript support without a compile step.

```bash
curl -fsSL https://bun.sh/install | bash
```

## Database: bbolt

[go.etcd.io/bbolt](https://pkg.go.dev/go.etcd.io/bbolt) — embedded key-value store. Pure Go, single-file database (`~/.trd/state.db`), transactional, zero external dependencies.

Used for: instance state, topic/secret indexes, user allowlist, persistent settings.

**Why bbolt:** No server to run, survives crashes (ACID), tiny footprint, perfect for single-process apps. The etcd project's fork of BoltDB.

```go
import bolt "go.etcd.io/bbolt"

db, _ := bolt.Open("state.db", 0o600, nil)
db.Update(func(tx *bolt.Tx) error {
    b, _ := tx.CreateBucketIfNotExists([]byte("data"))
    return b.Put([]byte("key"), []byte("value"))
})
```

## WebSocket: coder/websocket

[github.com/coder/websocket](https://pkg.go.dev/github.com/coder/websocket) (formerly nhooyr/websocket) — modern, context-aware WebSocket library. No CGo.

Used for: real-time communication between the dispatcher and channel plugins.

**Why this over gorilla/websocket:** Smaller API surface, context cancellation built in, actively maintained by Coder.

```go
import "github.com/coder/websocket"

conn, _ := websocket.Accept(w, r, nil)
conn.Write(ctx, websocket.MessageText, data)
```

## HTTP router: net/http stdlib

No framework. Go 1.22+ `http.ServeMux` supports path parameters (`/api/allowed/{username}`), which eliminates the need for chi/gorilla/echo for simple APIs.

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /api/items", handleList)
mux.HandleFunc("POST /api/items/{id}", handleCreate)
```

## UUID: google/uuid

[github.com/google/uuid](https://pkg.go.dev/github.com/google/uuid) — standard UUID v4 generation.

```go
id := uuid.NewString() // "550e8400-e29b-41d4-a716-446655440000"
```

## Speech-to-text: sherpa-onnx (whisper)

[github.com/k2-fsa/sherpa-onnx-go](https://github.com/k2-fsa/sherpa-onnx-go) — C++ inference engine with Go bindings (CGo). Runs ONNX models for speech recognition and TTS. The shared libraries are bundled in the Go module — no system install needed.

Used for: embedded whisper transcription of voice messages.

**Why this over calling whisper CLI:** No subprocess overhead, model loaded once and reused, ~14s for 2.5 min audio on CPU.

```go
import sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

config := sherpa.OfflineRecognizerConfig{...}
recognizer := sherpa.NewOfflineRecognizer(&config)
stream := sherpa.NewOfflineStream(recognizer)
stream.AcceptWaveform(16000, samples)
recognizer.Decode(stream)
text := stream.GetResult().Text
```

**Models:** Download from [sherpa-onnx releases](https://github.com/k2-fsa/sherpa-onnx/releases/tag/asr-models). We use `sherpa-onnx-whisper-base.en` (~165MB, int8 quantized).

## Text-to-speech: sherpa-onnx (VITS piper)

Same library as above. Runs VITS piper voice models for neural TTS.

**Why this over kokoro/piper CLI:** Embedded in the binary, no Python, no subprocess, sub-200ms for short sentences on CPU.

```go
tts := sherpa.NewOfflineTts(&config)
audio := tts.GenerateWithConfig("Hello world", &genConfig, nil)
// audio.Samples is []float32, audio.SampleRate is int
```

**Models:** Download from [sherpa-onnx TTS models](https://github.com/k2-fsa/sherpa-onnx/releases/tag/tts-models). We use `vits-piper-en_US-lessac-medium` (~64MB).

## Audio codec: hraban/opus

[github.com/hraban/opus](https://pkg.go.dev/github.com/hraban/opus) — Go bindings for libopus (CGo). Encode and decode Opus audio.

Used for: decoding incoming Telegram voice messages (OGG/Opus → PCM) and encoding TTS output (PCM → OGG/Opus). Replaces ffmpeg entirely.

**Build dependency:** `apt install libopus-dev libopusfile-dev`

```go
import "github.com/hraban/opus"

dec, _ := opus.NewDecoder(48000, 1)
n, _ := dec.Decode(packet, pcmBuffer)

enc, _ := opus.NewEncoder(48000, 1, opus.AppVoIP)
n, _ := enc.EncodeFloat32(samples, outputBuffer)
```

## MCP SDK: @modelcontextprotocol/sdk

[@modelcontextprotocol/sdk](https://www.npmjs.com/package/@modelcontextprotocol/sdk) — official MCP server SDK for TypeScript. Used by the channel plugin to expose tools and send notifications to Claude Code.

```typescript
import { Server } from "@modelcontextprotocol/sdk/server/index.js";

const server = new Server(
  { name: "my-plugin", version: "0.1.0" },
  { capabilities: { tools: {}, experimental: { "claude/channel": {} } } }
);
```

## Process management: tmux

Not a library — a system tool. Each Claude instance runs in a named tmux session (`trd-<instance-id>`). Managed via `os/exec` calls to `tmux new-session`, `tmux has-session`, `tmux kill-session`, `tmux capture-pane`, `tmux send-keys`.

**Why tmux:** Process isolation, survives SSH disconnects, pane capture for `/watch`, send-keys for auto-confirm. Already a common dev tool.

## Telegram: hand-rolled net/http client

No third-party Telegram library. A minimal `internal/telegram` package (~300 lines) wraps only the Bot API methods TRD actually uses: `getMe`, `getUpdates`, `sendMessage`, `editMessageText`, `setReaction`, `sendDocument`, `sendPhoto`, `sendVoice`, `sendAudio`, `getFile`.

**Why no library:** Keeps the dependency tree small, easy to understand, only implements what's needed. Long-polling is a simple loop.

## Secret management

No external secret store. Secrets are:
- **Bot token:** env var → persisted to bbolt settings bucket.
- **Instance secrets:** 256-bit random hex, written to `.trd/config.json` (mode 0600).
- **All WS traffic:** localhost only (`127.0.0.1`).

For production deployments, set env vars via your secret manager of choice and let TRD persist them to bbolt on first start.

## Summary table

| Concern | Choice | Import/Install |
|---------|--------|----------------|
| Language (main) | Go 1.23 | go.dev/dl |
| Language (plugin) | TypeScript + Bun | bun.sh |
| Database | bbolt | `go.etcd.io/bbolt` |
| WebSocket | coder/websocket | `github.com/coder/websocket` |
| HTTP router | stdlib net/http | built-in |
| UUID | google/uuid | `github.com/google/uuid` |
| Speech-to-text | sherpa-onnx (whisper) | `github.com/k2-fsa/sherpa-onnx-go` |
| Text-to-speech | sherpa-onnx (VITS piper) | same as above |
| Audio codec | hraban/opus | `github.com/hraban/opus` + libopus-dev |
| MCP server | @modelcontextprotocol/sdk | npm |
| Process mgmt | tmux | system package |
| Telegram API | hand-rolled net/http | internal package |
| Secrets | env vars + bbolt | no external deps |
