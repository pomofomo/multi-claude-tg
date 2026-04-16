# Architecture

TRD is a Go binary that bridges a Telegram supergroup with multiple Claude Code instances. Each forum topic maps to one repo. This document explains the high-level design, message flows, and key decisions.

## System overview

```
Telegram Supergroup                    trd (Go binary)
├── Topic: backend   ──┐          ┌── Telegram Bot (long-poll)
├── Topic: frontend  ──┼─────────>├── HTTP/WS server (localhost:7777)
├── Topic: mobile    ──┤          ├── bbolt DB (~/.trd/state.db)
└── /start <repo>    ──┘          ├── Health loop (30s)
                                  └── tmux process manager
                                           │
                        ┌──────────────────┼──────────────────┐
                    tmux: backend       tmux: frontend     tmux: mobile
                    claude --continue   claude --continue  claude --continue
                    (channel plugin)    (channel plugin)   (channel plugin)
                        │                   │                  │
                    reads .trd/         reads .trd/        reads .trd/
                    config.json         config.json        config.json
                    (secret+port)       (secret+port)      (secret+port)
```

## Three components

### 1. Dispatcher (`internal/dispatcher/dispatcher.go`)

The hub. A single long-running Go process that:

- **Long-polls Telegram** for messages, routes them by `message_thread_id` to the correct instance.
- **Serves a WebSocket endpoint** (`127.0.0.1:7777/channel`) that channel plugins connect to.
- **Manages tmux sessions** — spawns, monitors, restarts Claude instances.
- **Stores state in bbolt** — instance mappings, user allowlist, settings.
- **Handles all Telegram API calls** — the channel plugin never talks to Telegram directly.

### 2. Channel plugin (`channel/index.ts`)

A thin MCP server (~470 lines of TypeScript) that runs inside each Claude session:

- Reads `.trd/config.json` for identity (instance_id, secret, port).
- Opens a WebSocket to the dispatcher, authenticates with the secret.
- Converts dispatcher frames into `claude/channel` MCP notifications.
- Exposes tools: `reply`, `react`, `edit_message`, `download_attachment`, `send_voice`.
- Includes behavioral instructions (acknowledge messages, reply when done, ask questions).

### 3. CLI (`cmd/trd/main.go`)

Subcommand dispatch for managing the dispatcher and instances:

- `trd start` — runs the dispatcher.
- `trd status/list` — shows all instances with tmux + channel state.
- `trd stop/shell/cd/watch` — instance management.
- `trd allow/deny/allowed` — user allowlist management.

## Message flow

### Inbound (Telegram → Claude)

```
User types in Topic:backend
  → Telegram delivers Update{message, message_thread_id}
  → Dispatcher looks up (chat_id, thread_id) → instance_id in bbolt
  → Dispatcher pushes ws.Frame{type:"message"} to the channel plugin
  → Channel plugin emits MCP notification "claude/channel"
  → Claude sees the message, processes it
```

### Outbound (Claude → Telegram)

```
Claude calls reply(text, files) tool
  → Channel plugin sends ws.Frame{type:"reply"} to dispatcher
  → Dispatcher calls Telegram sendMessage / sendPhoto / sendVoice
  → Message appears in the topic
```

### Voice messages

```
User sends voice note in topic
  → Telegram delivers Update with Voice attachment
  → Dispatcher downloads OGG → Go Opus decoder → PCM
  → sherpa-onnx whisper transcribes in-process
  → Transcript sent as frame text to Claude (audio still attached)
```

```
Claude calls send_voice("text to speak")
  → Dispatcher's sherpa-onnx VITS synthesizes PCM
  → Go Opus encoder → OGG file
  → Dispatcher calls Telegram sendVoice
```

## Identity and persistence

### First connection

1. User sends `/start git@github.com:org/repo.git` in a topic.
2. Dispatcher generates UUID `instance_id` + 256-bit hex `secret`.
3. `git clone` into `~/.trd/repos/<instance-id>/`.
4. Writes `.trd/config.json` (mode 0600) and `.mcp.json`.
5. Spawns tmux session running `claude --continue --dangerously-skip-permissions --dangerously-load-development-channels server:trd-channel`.
6. Stores in bbolt with three indexes: `instance_id`, `chat_id:thread_id`, `secret`.

### Reconnection

- Channel plugin reads `.trd/config.json` → same secret → connects to dispatcher → mapped to correct topic. No re-registration.
- Dispatcher restart: `resumeInstances` relaunches dead tmux sessions. Channel plugins reconnect automatically (exponential backoff, 500ms → 10s).
- Claude restart (`/restart`): resumes previous conversation via `--continue` flag.
- Fresh start (`/reset`): launches Claude without `--continue`.

### Health and auto-recovery

- Health loop runs every 30s.
- Dead tmux sessions → restart (3 failures → `StateFailed`, notify topic).
- Rate-limit detection → auto-dismisses prompt, notifies topic, detects recovery.
- Attachment sweep → deletes files older than 7 days.

## Storage

bbolt with five buckets:

| Bucket | Key | Value | Purpose |
|--------|-----|-------|---------|
| `instances` | instance_id | JSON Instance | Primary store |
| `by_topic` | chat_id:thread_id | instance_id | Topic lookup |
| `by_secret` | secret | instance_id | Auth lookup |
| `allowed_users` | username | "1" | User allowlist |
| `settings` | env var name | value | Persistent config |

`Put` transactionally cleans stale index entries when a row changes.

## WebSocket wire protocol

One JSON frame per WS message. The `ws.Frame` struct is the union type.

**Server → plugin:** `message`, `download_result`, `tts_result`

**Plugin → server:** `hello`, `reply`, `react`, `edit`, `download`, `tts`

## Key design decisions

- **Dispatcher does all Telegram calls.** The channel plugin is stateless — it never talks to Telegram. This means instances survive dispatcher restarts cleanly.
- **One topic = one repo.** Enforced — `/start` in an already-bound topic is rejected.
- **Forum supergroup required.** Non-forum chats are rejected with a helpful error.
- **Secrets in `.trd/config.json` (0600).** The secret grants impersonation. `.trd/` is auto-gitignored.
- **WS on localhost only.** No external network exposure.
- **Env vars persisted to bbolt.** First start saves config; future restarts need no env vars.
- **Voice processing embedded.** Whisper STT and VITS TTS run in-process via sherpa-onnx CGo bindings. No external CLI tools.

## Roadmap

Open items (roughly prioritized):

- Auto-download inbound photos (pre-fetch instead of download_attachment dance)
- CI / release automation (GitHub Actions → npm publish)
- Web dashboard for monitoring instances
- Branch-aware topics (git worktrees)
- Remote instances via SSH
