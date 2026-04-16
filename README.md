# telegram-repo-dispatcher (`trd`)

Talk to multiple [Claude Code](https://docs.claude.com/claude-code) instances from one Telegram supergroup. **Each topic = one repo.** Create a topic, paste a git URL, and you're coding.

**This project is meant to be forked and modified.** Clone it, hack on it through Telegram (yes, using TRD itself), and make it your own. PRs and issues are welcome — see [Contributing](#contributing) below.

See [SPEC.md](./SPEC.md) for the full architecture, [QUESTIONS.md](./QUESTIONS.md) for MVP design decisions, and [TODO.md](./TODO.md) for the roadmap.

## What it is

A single Go binary (`trd`) that:

- Connects to a Telegram bot you own.
- For each forum topic, clones a repo and launches `claude` in a tmux session.
- Bridges Telegram messages ↔ Claude via a small Bun/TypeScript MCP channel plugin.
- Persists topic ↔ repo mappings in bbolt, so instances survive restarts.

## Prerequisites

| Tool | Why | Install hint |
|------|-----|--------------|
| `git` with SSH key | clones private repos | `apt install git` / `brew install git` |
| `tmux` | process isolation for each Claude | `apt install tmux` / `brew install tmux` |
| `bun` | runs the channel plugin (MCP server) | `curl -fsSL https://bun.sh/install \| bash` |
| `claude` (Claude Code CLI) | the thing being talked to | `npm i -g @anthropic-ai/claude-code` |
| Go 1.22+ *(dev only)* | build from source (CGo required) | [go.dev/dl](https://go.dev/dl) |
| `libopus-dev` *(build only)* | Opus audio codec for voice | `apt install libopus-dev libopusfile-dev` / `brew install opus opusfile` |

Run `bash scripts/install.sh` for an interactive prerequisite check — it tells you what's missing and how to install it on your platform.


## What installs where

TRD has two install scopes:

| Scope | What | Where | Who creates it |
|-------|------|-------|----------------|
| **User-level** | `trd` binary (dispatcher) | `~/.local/bin/trd` or npm global | You, once |
| **User-level** | `trd-channel` (MCP plugin) | npm global bin or `channel/index.ts` (dev) | You, once |
| **User-level** | State directory | `~/.trd/` (state.db, trd.log, attachments/) | `trd start`, auto |
| **Per-repo** | Identity file | `<repo>/.trd/config.json` (mode 0600) | `/start` command, auto |
| **Per-repo** | MCP config | `<repo>/.mcp.json` (merged, not clobbered) | `/start` command, auto |

The dispatcher is a long-running process you start once. Per-repo files are created automatically when you `/start` a repo from Telegram — you never touch them.

## Install

### From source (recommended for now)

```bash
git clone https://github.com/pomofomo/multi-claude-tg.git
cd multi-claude-tg

# 1. Build the dispatcher binary. (Puts in in $HOME/.local/bin which should be in $PATH)
make install                 # produces bin/trd

# 2. Install the channel plugin's deps.
cd channel && bun install && cd ..

# 3. Tell the dispatcher where the channel plugin lives.
#    On /start, trd writes each repo's .mcp.json to point at this.
#    Default (npm install): resolves `trd-channel` from PATH.
#    From source: set this env var so it uses your local checkout.
export TRD_CHANNEL_ENTRY="$PWD/channel/index.ts"
```

## Create the Telegram bot

1. Talk to [@BotFather](https://t.me/BotFather). `/newbot`, grab the token.
2. Create a **supergroup** and enable **Topics** in group settings (Telegram calls this "Forum").
3. Add the bot to the group and promote it to **admin** (so it can send to topics and read messages).
4. In the group's privacy settings, ensure "Admins can see all messages" (or equivalent) so the bot receives non-command messages as well.

## Run the dispatcher

Run `trd` in a dedicated tmux session so it survives SSH disconnects:

```bash
# Start a tmux session for the dispatcher (one-time)
tmux new-session -d -s trd

# Inside the session, start the dispatcher
tmux send-keys -t trd 'export TELEGRAM_BOT_TOKEN=123456:ABCDEF...' Enter
tmux send-keys -t trd 'export TRD_CHANNEL_ENTRY="$PWD/channel/index.ts"' Enter
tmux send-keys -t trd 'trd start' Enter

# Attach to see logs
tmux attach -t trd
# Ctrl+B, D to detach without stopping
```

## Restarting after code changes

When you modify TRD's source (the Go binary or channel plugin), rebuild
and restart the dispatcher. **Claude instances keep running** — the channel
plugin automatically reconnects to the new dispatcher (exponential backoff,
500ms → 10s).

```bash
make install              # rebuild + copy to ~/.local/bin/trd

make restart

# (This is what restart does internally)
tmux send-keys -t trd C-c
tmux send-keys -t trd 'trd start' Enter
```

The dispatcher's `resumeInstances` relaunches any tmux sessions that died
while it was down. Channel plugins inside running Claude instances reconnect
automatically within seconds — no need to restart them.

## Usage from Telegram

Inside a topic:

| Command | Effect |
|---------|--------|
| `/start git@github.com:me/repo.git` | Clones the repo, writes `.trd/config.json`, launches a tmux-managed `claude` bound to this topic |
| `/stop` | Kills the tmux session. Mapping kept; `/restart` brings it back |
| `/restart` | Relaunches tmux for the existing mapping |
| `/status` | Shows tmux + channel connection state |
| `/watch` | Captures the current tmux pane and replies with it |
| `/forget` | Deletes the mapping (keeps cloned files on disk) |

Anything that's not a slash command gets forwarded to Claude in that topic.

**Note** When you start a repo fro the first time in a new topic, it will start claude in that directory for the first time. And you need to manually click approval. (This may get better soon)

## Local CLI

All commands accept a **repo name** or **instance-ID prefix** as `<name>`.

```bash
trd list               # all instances: repo name, state, tmux, channel, URL
trd status             # alias for list
trd shell <name>       # open $SHELL in the repo directory
trd cd <name>          # print the repo path (use: cd $(trd cd backend))
trd stop <name>        # kill the tmux session
trd watch <name>       # capture the current tmux pane
```

## User allowlist

By default, anyone who can write in the supergroup can interact with trd.
To restrict access to specific Telegram usernames:

```bash
trd allow alice        # add @alice to the allowlist
trd allow bob          # add @bob
trd allowed            # show current allowlist
trd deny bob           # remove @bob
```

When the allowlist is non-empty, only listed users can send commands or
messages. An empty allowlist means everyone is allowed (backwards compatible).

You can also seed the allowlist via environment variable:

```bash
export TRD_ALLOWED_USERNAMES=alice,bob
```

The env var is merged with the stored list. The stored list persists in
bbolt across restarts; the env var does not.

## File layout

```
~/.trd/
├── state.db              # bbolt: instances + topic/secret indexes
├── trd.log               # dispatcher logs (JSON/text lines)
├── attachments/          # Telegram downloads
└── repos/
    └── <instance-id>/    # cloned repo
        ├── .trd/config.json   # {instance_id, secret, dispatcher_port} (mode 0600)
        └── .mcp.json          # makes Claude spawn trd-channel on start
```

## How a message flows

```
You type in Topic:backend
  ↳ Telegram delivers Update{message, message_thread_id} to trd
  ↳ trd looks up (chat_id, thread_id) → instance_id in bbolt
  ↳ trd pushes frame over WS to the channel plugin for that instance
  ↳ Channel plugin emits MCP notification "claude/channel" to Claude
  ↳ Claude processes, calls `reply` tool
  ↳ Channel plugin forwards frame back to trd via WS
  ↳ trd sendMessage to Telegram with message_thread_id
```

## Optional: voice messages & TTS

TRD can transcribe incoming voice messages (Whisper) and send spoken replies (TTS).
Both are **optional** — if not configured, voice messages are forwarded as audio
attachments and the `send_voice` tool returns a clear error.

### Installing models

Both whisper (speech-to-text) and TTS are embedded in the `trd` binary via
[sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) — no external CLI tools
needed. Just download the models (~230MB total):

```bash
make install-models
```

This downloads:
- **Whisper base.en** (English, ~165MB) → `~/.trd/models/whisper/`
- **VITS piper lessac-medium** (English TTS, ~64MB) → `~/.trd/models/tts/`

Models are auto-detected at `~/.trd/models/` on startup. Override with env vars
if you want a different location:

```bash
export TRD_WHISPER_MODEL_DIR=/path/to/whisper/model
export TRD_TTS_MODEL_DIR=/path/to/tts/model
```

### Configuration reference

| Feature | Env var | Default |
|---------|---------|---------|
| **Whisper (embedded)** | `TRD_WHISPER_MODEL_DIR` | `~/.trd/models/whisper/` |
| **TTS (embedded)** | `TRD_TTS_MODEL_DIR` | `~/.trd/models/tts/` |
| **OpenAI API** (fallback) | `TRD_OPENAI_API_KEY` | — (used when models not installed) |

**Whisper flow:** voice/audio message → dispatcher downloads OGG → Go Opus
decoder extracts PCM → sherpa-onnx whisper transcribes in-process → sends
transcript as the message text to Claude (original audio still attached).

**TTS flow:** Claude calls `send_voice` tool with text → sherpa-onnx VITS
synthesizes PCM in-process → Go Opus encoder writes OGG → sent as Telegram
voice message.

No ffmpeg or other external audio tools needed — everything is compiled into
the `trd` binary.

**Smart outbound media:** when Claude attaches files in `reply`, the dispatcher
detects the file type and uses the appropriate Telegram method:
- `.jpg`/`.png`/`.gif`/`.webp` → `sendPhoto` (inline image)
- `.ogg`/`.opus` → `sendVoice` (inline audio player)
- `.mp3`/`.m4a`/`.wav` → `sendAudio` (music player)
- everything else → `sendDocument`

## Development

```bash
make test           # go test ./...
make build-all      # cross-compile for linux/darwin × amd64/arm64 into bin/
make lint           # go vet
```

## Security model (MVP)

- **Authorization:** membership in the Telegram supergroup = authorized to spawn Claude. Operator-level trust.
- **Secrets:** per-instance 256-bit secret in `.trd/config.json`, mode 0600. Channel plugin authenticates to the dispatcher with it; unknown secrets are rejected.
- **Networking:** dispatcher listens on `127.0.0.1` only. No external exposure.
- **Private repos:** rely on your local SSH agent / `~/.ssh/` config.

## Not (yet) implemented

These are on the roadmap but deliberately out of MVP scope (see SPEC.md § Future considerations, [TODO.md](./TODO.md) for the full list):

- Web dashboard for monitoring instances
- Branch-aware topics (git worktrees)
- Chat history persistence across Claude restarts
- Remote instances over SSH
- CI / release automation
- Auto-download inbound photos (currently uses the two-step `download_attachment` dance)

## Contributing

TRD is designed to be forked and extended. The codebase is small enough to
understand in an afternoon.

**How to contribute:**

1. Fork this repo and clone it.
2. Set up TRD pointing at your fork — you can literally develop it through
   Telegram, using TRD to manage its own repo.
3. Make your changes, test with `make test`, and submit a PR.

**Where to look:**

- [TODO.md](./TODO.md) — roadmap with checked/unchecked items
- [SPEC.md](./SPEC.md) — architecture and design rationale
- [QUESTIONS.md](./QUESTIONS.md) — design decisions and trade-offs
- `internal/dispatcher/dispatcher.go` — the hub (start here)
- `channel/index.ts` — the MCP channel plugin (~470 lines)

Issues and feature requests are encouraged.

## License

MIT — see [LICENSE](./LICENSE).
