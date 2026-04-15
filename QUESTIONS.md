# Design Decisions (MVP)

SPEC.md left several choices open. This file records what was decided and why, so the choices are legible to future contributors.

## 1. Repo clone path: by instance ID, not by URL

**Question:** SPEC shows two conflicting layouts for cloned repos:
- Prose: `~/.trd/repos/<repo_url>/` (e.g. `~/.trd/repos/github.com/pomofomo/multi-claude-tg/`)
- File layout diagram: `~/.trd/repos/<instance-id-1>/`

**Decision:** Use `~/.trd/repos/<instance-id>/`.

**Why:** Two topics pointing at the same repo URL (e.g. one for `main`, one for an experiment) would collide. Instance-ID paths isolate them. Telegram topic name is not guaranteed unique/stable either.

## 2. Go Telegram library

**Question:** Which Telegram Bot API client?

**Decision:** `github.com/go-telegram/bot` (`bot.Bot`).

**Why:** Actively maintained, supports forum topic `message_thread_id` natively, long-polling built in, zero-config handler pattern. Alternative `go-telegram-bot-api/v5` is fine but slightly older-style.

## 3. WebSocket library

**Question:** gorilla/websocket, coder/websocket, or raw?

**Decision:** `github.com/coder/websocket` (formerly nhooyr/websocket).

**Why:** Context-aware API, smaller surface, modern. No CGO.

## 4. CLI framework

**Question:** cobra, urfave/cli, or stdlib `flag`?

**Decision:** stdlib `flag` + small subcommand dispatch.

**Why:** Only 4 subcommands (start/status/stop/logs). Cobra would be overkill and add ~500 KB to the binary.

## 5. bbolt schema

**Question:** How to map topics ↔ instances ↔ secrets?

**Decision:**
- Bucket `instances`: key = `instance_id` (UUID), value = JSON `{TopicID, ChatID, RepoURL, RepoPath, Secret, State, CreatedAt}`
- Bucket `by_topic`: key = `chat_id:thread_id`, value = `instance_id`
- Bucket `by_secret`: key = `secret`, value = `instance_id`

**Why:** Single write per create, O(1) lookups from any direction, no schema migrations needed for MVP.

## 6. Instance ID + secret format

**Decision:**
- Instance ID: UUID v4 (`google/uuid`).
- Secret: 32 random bytes, hex-encoded (64 chars). Generated with `crypto/rand`.

## 7. Config file permissions

**Question:** `.trd/config.json` contains the dispatcher secret — protect it?

**Decision:** Write with mode `0600`. Add `.trd/` to the repo's `.gitignore` automatically after first clone (append if missing).

**Why:** The secret grants impersonation of that instance. Keep it off-disk-readable by other users and out of git.

## 8. Supergroup vs DM

**Question:** Does TRD require a supergroup with topics, or work in DMs/basic groups too?

**Decision:** Requires a **forum supergroup** (topics enabled). Reject non-forum chats with a helpful error.

**Why:** The whole model is "topic = repo." Basic groups have no `message_thread_id`.

## 9. Authorization model

**Question:** Who is allowed to send `/start <repo>` and run code?

**Decision:** MVP trust model is **"membership in the supergroup = authorized."** The bot owner controls the group; anyone they invite can spawn Claude instances on the host.

**Why:** Keeps MVP simple. Operator-level security. A per-user allowlist is a natural follow-up (see Future Considerations in SPEC).

## 10. One-topic-one-repo enforcement

**Question:** What happens when a user sends `/start` in a topic that already has an instance?

**Decision:** Reject with `This topic is already bound to <repo>. Use /stop first.` Do not overwrite.

## 11. `/stop` vs `/restart` semantics

**Decision:**
- `/stop` kills the tmux session but keeps the bbolt mapping + config. State becomes `stopped`.
- `/restart` relaunches tmux for the existing mapping. State becomes `running`.
- The mapping is only deleted if the user explicitly types `/forget` (not in SPEC — added as escape hatch to clear corrupted entries).

## 12. Claude invocation string

**Question:** What command does tmux run?

**Decision:**
```
claude --dangerously-load-development-channels server:trd-channel
```
with `CLAUDE_TRD_CONFIG=<abs-path-to-.trd/config.json>` in the env (used by the channel plugin to locate config).

The channel plugin is registered as the MCP server `trd-channel` via the repo's `.mcp.json` (written alongside `.trd/config.json` on first clone).

## 13. Channel plugin ↔ Claude protocol

**Question:** SPEC says "Channel plugin emits `claude/channel` notification." What does that mean concretely?

**Decision (MVP interpretation):** The channel plugin is an MCP server that:
1. Sends incoming Telegram messages to Claude as prompts via MCP `notifications/message` with method `claude/channel` and payload `{source: "telegram", chat_id, message_id, user, ts, text}`.
2. Exposes MCP tools that Claude calls to respond:
   - `reply(chat_id, text, reply_to?, files?)` — send a message
   - `react(chat_id, message_id, emoji)` — add a reaction
   - `edit_message(chat_id, message_id, text)` — edit an existing message
3. All tool calls are forwarded over the WebSocket to the dispatcher, which performs the actual Telegram API calls.

**Why:** This mirrors the MCP telegram plugin that already exists in Claude Code — same mental model, minimal reinvention. If Claude Code's exact channel notification method differs, the plugin is one file to tweak.

## 14. Binary distribution via npm

**Question:** How does `npm install -g telegram-repo-dispatcher` ship a Go binary?

**Decision:** `postinstall.js` detects `process.platform` + `process.arch`, resolves to one of `bin/trd-{linux,darwin}-{amd64,arm64}`, and symlinks it to the npm `bin/` entry. The four prebuilt binaries are checked into the package at release time (or fetched from a GitHub release). For MVP the repo ships with a build script and expects the developer to run it locally; real release automation is a follow-up.

## 15. Install script scope

**Question:** SPEC asks for an install script that checks/installs prerequisites on Linux/macOS/WSL.

**Decision:** `scripts/install.sh` is a **check-and-prompt** script, not an auto-installer. It:
- Detects OS (linux, darwin, wsl).
- Checks for: `git`, `tmux`, `bun`, `claude` (Claude Code CLI), SSH key presence.
- For each missing tool: prints the right install command for the detected OS (brew/apt/pacman/dnf) and asks the user to run it.
- Does not `sudo` silently. Asks for confirmation before any state-changing action.

**Why:** Auto-installing system packages across distros without user consent is the kind of script that ruins someone's afternoon. Check + prompt is the safe default.

## 16. Logs

**Decision:** Dispatcher logs to stdout **and** appends to `~/.trd/trd.log` (line-oriented JSON). `trd logs <topic>` tails the tmux pane via `tmux capture-pane -p -t trd-<instance-id>`.

## 17. Graceful shutdown

**Decision:** SIGINT/SIGTERM → close Telegram long-poll, close WS server, flush bbolt, exit. Tmux sessions are **not** killed — they survive and reconnect on next `trd start`. This matches the "instances survive restarts" principle.

## 18. Health-check cadence

**Decision:** 30-second interval. Configurable via `TRD_HEALTH_INTERVAL_SEC` env var.

## 19. Go version

**Decision:** Go 1.22 (generics, slices stdlib).

## 20. Testing scope for MVP

**Decision:** Unit tests for: storage (bbolt round-trip), config (read/write/gitignore append), pubsub (fan-in/fan-out), url sanitization. Integration tests are left for post-MVP — they need a real Telegram bot token and a real tmux.
