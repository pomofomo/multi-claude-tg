# CLAUDE.md

Key context for Claude Code when editing this repo. For full details see [ARCHITECTURE.md](./ARCHITECTURE.md) and [DEV.md](./DEV.md).

## What this is

`trd` (Telegram Repo Dispatcher) routes between a Telegram supergroup and multiple Claude Code instances. Each forum topic = one repo. `/start <git-url>` clones it, spawns `claude` in tmux, and bridges messages both ways.

## Build / test / run

```bash
make build              # CGO_ENABLED=1 go build → bin/trd
make install            # build + copy to ~/.local/bin/trd
make test               # go test ./...
make lint               # go vet ./...
make restart            # rebuild + restart dispatcher in tmux
make install-models     # download whisper + TTS models (~230MB)
```

Channel plugin:
```bash
cd channel && bun install
bunx tsc --noEmit       # typecheck
```

## Key files

| File | Role |
|------|------|
| `cmd/trd/main.go` | CLI entry, subcommand dispatch, settings persistence |
| `internal/dispatcher/dispatcher.go` | **The hub** — Telegram poll, WS server, tmux manager, health loop, rate-limit watchdog, command handlers |
| `internal/media/media.go` | Whisper STT + VITS TTS via sherpa-onnx (CGo), OpenAI API fallback |
| `internal/audio/audio.go` | OGG/Opus decode/encode (replaces ffmpeg) |
| `internal/storage/storage.go` | bbolt: instances, topic/secret indexes, allowlist, settings |
| `internal/ws/ws.go` | WebSocket server, Frame protocol, HTTP API endpoints |
| `internal/telegram/telegram.go` | Minimal hand-rolled Telegram Bot API client |
| `internal/tmuxmgr/tmuxmgr.go` | tmux session lifecycle |
| `channel/index.ts` | MCP channel plugin — WS bridge + behavioral instructions |

## Package dependency graph

```
cmd/trd → dispatcher → storage, telegram, tmuxmgr, ws, config, media
                       media → audio
```

No cycles. Leaf packages (`config`, `storage`, `audio`, `telegram`, `tmuxmgr`) have no internal deps.

## Dispatcher command handlers

Telegram: `cmdStart`, `cmdStop`, `cmdRestart`, `cmdReset`, `cmdStatus`, `cmdWatch`, `cmdDebug`, `cmdForget`. Non-commands → `routeToInstance`.

## WS frame types

Server→plugin: `message`, `download_result`, `tts_result`. Plugin→server: `hello`, `reply`, `react`, `edit`, `download`, `tts`.

## Storage buckets

`instances`, `by_topic`, `by_secret`, `allowed_users`, `settings`. Use the Store methods — don't access buckets directly.

## Restart workflow

```bash
make restart    # rebuilds binary, Ctrl+C dispatcher, starts again
```

Claude instances keep running. Channel plugins reconnect automatically (exponential backoff). `resumeInstances` relaunches dead tmux sessions on dispatcher start. `/restart` resumes conversation via `--continue`. `/reset` starts fresh.

## Conventions

- **Dispatcher does all Telegram API calls.** Channel plugin is a stateless WS↔MCP bridge.
- **Channel plugin stays thin.** Add logic to the Go dispatcher, route via new frame types.
- **tmux session names:** `trd-<instance-id>` — `tmuxmgr.SessionName` is the source of truth.
- **Secrets:** `.trd/config.json` mode 0600, WS on `127.0.0.1` only.
- **`.trd/` is auto-gitignored** in cloned repos.
- **CGo required** for sherpa-onnx (whisper + TTS) and libopus (audio codec).
- **Env vars are persisted** to bbolt settings bucket on first start. Future restarts read from DB.

## Debugging

| Source | Location |
|--------|----------|
| Dispatcher | `tmux attach -t trd` or `~/.trd/trd.log` |
| Channel plugin | `/tmp/trd-channel.log` |
| Claude Code | `~/.claude/debug/<session-id>.txt` |

Toggle verbose: `/debug` in Telegram or `trd start --debug`.
