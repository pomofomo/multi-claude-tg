# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

`trd` (Telegram Repo Dispatcher) routes between a Telegram supergroup and multiple Claude Code instances. Every forum topic maps to one repo; `/start <git-url>` in a topic clones it, spawns `claude` in tmux, and bridges messages both ways. See `SPEC.md` for the full architecture and `QUESTIONS.md` for MVP decisions.

## Build / test / run

```bash
make build              # -> bin/trd
make test               # go test ./...
make lint               # go vet ./...
make build-all          # cross-compile to bin/trd-{linux,darwin}-{amd64,arm64}

go test ./internal/storage -run TestPutGetByAllThreeIndexes   # single test
```

Running locally:
```bash
export TELEGRAM_BOT_TOKEN=...
./bin/trd start         # default port 7777
./bin/trd status | stop <prefix> | watch <prefix>
```

Channel plugin (Bun/MCP):
```bash
cd channel && bun install
TRD_CONFIG=/path/to/.trd/config.json bun run index.ts
bunx tsc --noEmit       # typecheck
```

## Architecture (read these three together)

1. **`cmd/trd/main.go`** — subcommand dispatch, `start` constructs `dispatcher.Dispatcher` and `Run`s it.
2. **`internal/dispatcher/dispatcher.go`** — the hub. Owns the Telegram long-poll loop, WS server, tmux process manager, health loop, and bbolt store. Command handlers (`cmdStart`, `cmdStop`, `cmdRestart`, `cmdStatus`, `cmdWatch`, `cmdForget`) route per-topic; non-command messages are forwarded to the instance's channel plugin via a buffered frame channel. User allowlist enforcement happens here (`isUserAllowed`).
3. **`channel/index.ts`** — the MCP server Claude Code loads as a "channel." Reads `$TRD_CONFIG` for its identity, opens a WebSocket to the dispatcher, turns inbound frames into `claude/channel` MCP notifications, exposes `reply` / `react` / `edit_message` / `download_attachment` / `send_voice` tools that forward back over the WS. Behavioral instructions (acknowledge, reply-when-done, ask questions, parallel execution) are shipped in the `instructions` field.

**Key invariant:** the dispatcher performs *all* real Telegram Bot API calls. The channel plugin never talks to Telegram directly — it's a thin MCP↔WS bridge. This lets instances survive restarts: the plugin has no state, the dispatcher has all of it.

### Identity & persistence flow

- `/start` → generate UUID instance_id + 32-byte hex secret → `git clone` into `~/.trd/repos/<instance-id>/` → write `.trd/config.json` (mode 0600) → write `.mcp.json` pointing Claude at `trd-channel` → `tmux new-session` with `TRD_CONFIG` env → `Store.Put(inst)` with three indexes (`instance_id`, `chat_id:thread_id`, `secret`).
- Plugin reconnects by presenting its secret; dispatcher's `AuthSecret` looks it up. No re-registration.
- Health loop (30s) restarts dead tmux sessions; 3 failures → `StateFailed` + notify topic.

### Restart workflow

The dispatcher runs in a tmux session. To rebuild and restart after code changes:

```bash
make install              # rebuild + copy to ~/.local/bin/trd
tmux send-keys -t trd C-c  # stop the running dispatcher
tmux send-keys -t trd 'trd start' Enter  # restart
```

**Claude instances don't need restarting.** The channel plugin reconnects automatically (exponential backoff, 500ms → 10s). The dispatcher's `resumeInstances` relaunches any tmux sessions that died while it was down.

### Storage (`internal/storage/storage.go`)

bbolt with four buckets: `instances`, `by_topic` (chat_id:thread_id → instance_id), `by_secret`, `allowed_users`. `Put` transactionally cleans stale index entries if the row previously existed under a different secret/topic. Always use `Put`/`Get`/`ByTopic`/`BySecret` — don't poke buckets directly. Allowlist managed via `AddAllowedUser`/`RemoveAllowedUser`/`ListAllowedUsers`/`IsAllowedUser`.

### WebSocket wire protocol (`internal/ws/ws.go`)

One JSON frame per WS message. Server→plugin types: `message`, `download_result`, `tts_result`. Plugin→server types: `hello`, `reply`, `react`, `edit`, `download`, `tts`. The `ws.Frame` struct is the union; extend it there if adding a frame type and handle it in `dispatcher.OnOutbound`.

## Conventions that matter here

- **Go packages are layered top-down**: `cmd/trd` → `dispatcher` → (`storage`, `telegram`, `tmuxmgr`, `ws`, `config`). `config`, `storage`, `pubsub` are leaf packages with no internal deps. Don't introduce cycles.
- **Secrets** live in `.trd/config.json` at mode 0600, and the dispatcher WS listens on `127.0.0.1` only. Preserve both when editing auth paths.
- **`.trd/` is auto-gitignored** in cloned repos (`config.EnsureGitignore`). Don't remove.
- **tmux session names** are `trd-<instance-id>`. `tmuxmgr.SessionName` is the only place that format lives.
- **Telegram client** is a hand-rolled `net/http` wrapper (`internal/telegram`), not a third-party lib — keep it minimal (only methods TRD actually calls).
- **Channel plugin stays thin** (~470 lines). Don't put business logic there; add it to the Go dispatcher and route via a new frame type.

## Things not to break

- Instance state must survive dispatcher restarts: `resumeInstances` relaunches any `StateRunning` tmux that died. New instance lifecycle code must participate.
- The `channel/index.ts` `NOTIFY_METHOD` defaults to `claude/channel` — if Claude Code's channel notification method changes, this is the one knob to turn (or set `TRD_NOTIFY_METHOD` env var).
- `writeMCPConfig` in `dispatcher.go` resolves the channel plugin command: `$TRD_CHANNEL_ENTRY` → `bun run <path>`, otherwise `trd-channel` (npm bin). Both paths need to keep working.

## Distribution

npm package (`package.json` + `postinstall.js` + `bin/trd-shim.js`) ships four prebuilt Go binaries under `bin/trd-{os}-{arch}` and symlinks the matching one to `bin/trd`. `scripts/build-binaries.sh` produces them; `scripts/install.sh` is a prerequisite checker (check-and-prompt, never auto-sudo).
