# telegram-repo-dispatcher (`trd`)

Talk to multiple [Claude Code](https://docs.claude.com/claude-code) instances from one Telegram supergroup. **Each topic = one repo.** Create a topic, paste a git URL, and you're coding.

See [SPEC.md](./SPEC.md) for the full architecture, [QUESTIONS.md](./QUESTIONS.md) for MVP design decisions.

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
| Go 1.22+ *(dev only)* | build from source | [go.dev/dl](https://go.dev/dl) |

Run `bash scripts/install.sh` for an interactive prerequisite check — it tells you what's missing and how to install it on your platform.

## Install into your local Claude instance

These instructions assume **prerequisites are already prepared** (see above).

### Option A — Install from source (recommended for now)

```bash
git clone https://github.com/pomofomo/multi-claude-tg.git
cd multi-claude-tg

# 1. Build the dispatcher binary.
make build                 # produces bin/trd

# 2. Install the channel plugin's deps.
cd channel && bun install && cd ..

# 3. Tell Claude where to find the channel plugin at runtime.
#    The dispatcher writes a per-repo .mcp.json on /start that points at
#    the `trd-channel` binary. By default that resolves via the npm-installed
#    bin; when running from source, export:
export TRD_CHANNEL_ENTRY="$PWD/channel/index.ts"

# 4. Put the dispatcher on your PATH, or call it with an absolute path.
sudo ln -s "$PWD/bin/trd" /usr/local/bin/trd   # or symlink into ~/.local/bin
ln -s "$PWD/bin/trd" $HOME/.local/bin/trd
```

### Option B — Install as an npm package (once published)

```bash
npm install -g telegram-repo-dispatcher
# ships prebuilt Go binaries for linux/{amd64,arm64} and darwin/{amd64,arm64}
# postinstall falls back to `go build` if no prebuilt matches your platform
```

This puts `trd` and `trd-channel` on your `$PATH`.

## Create the Telegram bot

1. Talk to [@BotFather](https://t.me/BotFather). `/newbot`, grab the token.
2. Create a **supergroup** and enable **Topics** in group settings (Telegram calls this "Forum").
3. Add the bot to the group and promote it to **admin** (so it can send to topics and read messages).
4. In the group's privacy settings, ensure "Admins can see all messages" (or equivalent) so the bot receives non-command messages as well.

## Run the dispatcher

```bash
export TELEGRAM_BOT_TOKEN=123456:ABCDEF...
trd start                     # binds to 127.0.0.1:7777 by default
```

Run it in a long-lived tmux session, or drop a systemd user unit (example in `examples/` in a future release).

## Usage from Telegram

Inside a topic:

| Command | Effect |
|---------|--------|
| `/start git@github.com:me/repo.git` | Clones the repo, writes `.trd/config.json`, launches a tmux-managed `claude` bound to this topic |
| `/stop` | Kills the tmux session. Mapping kept; `/restart` brings it back |
| `/restart` | Relaunches tmux for the existing mapping |
| `/status` | Shows tmux + channel connection state |
| `/forget` | Deletes the mapping (keeps cloned files on disk) |

Anything that's not a slash command gets forwarded to Claude in that topic.

## Local CLI

```bash
trd status           # list all instances
trd stop <prefix>    # kill by instance-id prefix
trd logs <prefix>    # dump the current tmux pane for the instance
```

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

These are on the roadmap but deliberately out of MVP scope (see SPEC.md § Future considerations):

- Web dashboard for monitoring instances
- Branch-aware topics (git worktrees)
- Chat history persistence across Claude restarts
- Remote instances over SSH
- Per-user allowlist within a supergroup

## License

MIT — see [LICENSE](./LICENSE).
