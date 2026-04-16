# Developer Guide

How to contribute to TRD, build from source, and debug issues.

## Build and test

```bash
make build              # CGO_ENABLED=1 go build → bin/trd
make install            # build + copy to ~/.local/bin/trd
make test               # go test ./...
make lint               # go vet ./...
make install-models     # download whisper + TTS models (~230MB)
make restart            # rebuild + restart dispatcher in tmux
make start              # start dispatcher (reads saved config from DB)
```

First-time setup:

```bash
make setup TELEGRAM_BOT_TOKEN=123456:ABCDEF...
```

This builds, installs channel plugin deps, starts the dispatcher in a tmux session named `trd`, and saves config to the database.

### Build dependencies

| Package | Why | Install |
|---------|-----|---------|
| Go 1.22+ | compiles the dispatcher | [go.dev/dl](https://go.dev/dl) |
| `libopus-dev` | Opus codec for voice (CGo) | `apt install libopus-dev libopusfile-dev` |
| `bun` | runs the channel plugin | `curl -fsSL https://bun.sh/install \| bash` |

CGo is required (`CGO_ENABLED=1`) because of sherpa-onnx (whisper + TTS) and libopus.

## Code layout

```
cmd/trd/main.go                  CLI entry point, subcommand dispatch
internal/
  dispatcher/dispatcher.go       The hub — Telegram poll, WS, tmux, health loop
  ws/ws.go                       WebSocket server + Frame protocol
  storage/storage.go             bbolt wrapper (instances, allowlist, settings)
  media/media.go                 Whisper STT + VITS TTS (sherpa-onnx embedded)
  audio/audio.go                 OGG/Opus decode/encode (replaces ffmpeg)
  telegram/telegram.go           Minimal Telegram Bot API client
  tmuxmgr/tmuxmgr.go            tmux session management
  config/                        Paths, repo config, gitignore
channel/index.ts                 MCP channel plugin (Bun/TypeScript)
```

### Package dependency graph

```
cmd/trd → dispatcher → storage, telegram, tmuxmgr, ws, config, media
                       media → audio
```

Leaf packages (`config`, `storage`, `audio`, `telegram`, `tmuxmgr`) have no internal deps. Don't introduce cycles.

## Contributing

1. Fork this repo and clone it.
2. Set up TRD pointing at your fork — you can develop it through Telegram using TRD itself.
3. Make changes, test with `make test`, submit a PR.

### Self-development workflow

TRD can manage its own repo. After the initial setup:

1. Create a topic in your Telegram group.
2. Send `/start git@github.com:you/multi-claude-tg.git`.
3. Run `make self-modify NAME=multi-claude-tg` to point the channel plugin at the instance's checkout.
4. Now edits to `channel/index.ts` in the instance take effect on restart.

For Go changes, tell Claude to run `make restart` — it rebuilds the binary and restarts the dispatcher. Channel plugins reconnect automatically.

## Conventions

- **Telegram client** is a hand-rolled `net/http` wrapper — keep it minimal (only methods TRD actually calls).
- **Channel plugin stays thin** (~470 lines). Business logic goes in the Go dispatcher; route via a new WS frame type.
- **tmux session names** are `trd-<instance-id>`. `tmuxmgr.SessionName` is the single source of truth.
- **Secrets** in `.trd/config.json` at mode 0600. Dispatcher WS on `127.0.0.1` only.
- **`.trd/` is auto-gitignored** in cloned repos. Don't remove.

## Adding a new Telegram command

1. Add the case in `handleMessage`'s switch block in `dispatcher.go`.
2. Write the `cmd<Name>` handler method.
3. Update the README Telegram commands table.

## Adding a new WS frame type

1. Add the field(s) to `ws.Frame` if needed.
2. Handle the new `frame.Type` case in `dispatcher.OnOutbound`.
3. Add the corresponding tool in `channel/index.ts` (ListToolsRequestSchema + CallToolRequestSchema handlers).

## Debugging

Three log sources to check:

### 1. TRD dispatcher logs

```bash
tmux attach -t trd              # live logs (Ctrl+B, D to detach)
tail -f ~/.trd/trd.log          # from another terminal
trd start --debug               # verbose mode (or TRD_DEBUG=1)
```

Toggle debug at runtime: send `/debug` in any Telegram topic. New/restarted Claude instances will include/omit `--debug` accordingly.

### 2. Channel plugin logs

```bash
tail -f /tmp/trd-channel.log
```

Override path with `TRD_CHANNEL_LOG` env var. Shows: WS connect/disconnect, frame send/recv, MCP notification delivery, tool calls.

### 3. Claude Code debug logs

```bash
ls -lt ~/.claude/debug/                    # most recent session first
tail -100 ~/.claude/debug/<session-id>.txt
```

MCP protocol from Claude's side — useful when the plugin connects but messages aren't getting through.

### Quick debug checklist

| Symptom | Check |
|---------|-------|
| Message not arriving | `trd.log`: look for "tg recv" → "tg->claude forward" → "frame queued". If "no live channel", plugin isn't connected. |
| Plugin not connecting | `/tmp/trd-channel.log`: look for "ws connect" / "ws error". Verify `.trd/config.json` port + secret. |
| Claude not responding | `/watch` in the topic. Check for rate-limit prompts (watchdog auto-dismisses). |
| TTS/Whisper broken | `trd.log`: search for "whisper:" or "tts" entries. Verify models in `~/.trd/models/`. |
| Rate-limited | Watchdog auto-dismisses and notifies topic. Check `trd.log` for "rate-limit detected". |

## Environment variables

| Variable | Purpose | Persisted |
|----------|---------|-----------|
| `TELEGRAM_BOT_TOKEN` | Bot authentication | Yes |
| `TRD_PORT` | Dispatcher HTTP/WS port (default 7777) | No |
| `TRD_CHANNEL_ENTRY` | Path to channel plugin source | Yes |
| `TRD_WHISPER_MODEL_DIR` | Whisper model directory | Yes |
| `TRD_TTS_MODEL_DIR` | TTS model directory | Yes |
| `TRD_OPENAI_API_KEY` | OpenAI API fallback for STT/TTS | Yes |
| `TRD_ALLOWED_USERNAMES` | Comma-separated allowlist | Yes |
| `TRD_DEBUG` | Set to "1" for debug mode | No |
| `TRD_HEALTH_INTERVAL_SEC` | Health loop interval (default 30) | No |
| `TRD_CLAUDE_BIN` | Claude binary name (default "claude") | No |
| `TRD_CLAUDE_ARGS` | Override Claude arguments entirely | No |
| `TRD_CLAUDE_CONFIRM_KEYS` | Keys to send for dev-channels prompt (default "Enter") | No |

"Persisted" means saved to bbolt on first start and restored on future starts when the env var isn't set.

## Security model

- **Authorization:** Supergroup membership = authorized. User allowlist (`trd allow/deny`) adds fine-grained control.
- **Secrets:** Per-instance 256-bit secret in `.trd/config.json` (mode 0600). Unknown secrets rejected.
- **Networking:** Dispatcher on `127.0.0.1` only. No external exposure.
- **Private repos:** SSH agent / `~/.ssh/` config.
- **`.mcp.json`** is mode 0644 — it's just a pointer to the channel plugin, not a secret.
