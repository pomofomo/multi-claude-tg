# telegram-repo-dispatcher (`trd`)

Talk to multiple [Claude Code](https://docs.claude.com/claude-code) instances from one Telegram supergroup. **Each topic = one repo.** Create a topic, paste a git URL, and you're coding.

**This project is meant to be forked and modified.** Clone it, hack on it through Telegram (yes, using TRD itself), and make it your own. PRs and issues are very welcome.

## How it works

A single Go binary connects to your Telegram bot, and for each forum topic:

1. Clones a repo and launches `claude` in a tmux session.
2. Bridges Telegram messages to Claude via an MCP channel plugin.
3. Claude replies appear in the topic. Voice messages are transcribed; Claude can speak back.
4. Everything persists — restart the server, and all instances reconnect automatically.

Voice processing (whisper STT + VITS TTS) and Opus audio encoding are embedded directly in the binary. No ffmpeg, no Python, no external CLI tools.

## Quick start

```bash
# 1. Clone and build
git clone https://github.com/pomofomo/multi-claude-tg.git
cd multi-claude-tg

# 2. Check prerequisites (interactive — tells you what's missing)
make install-deps

# 3. Download voice models (optional, ~230MB)
make install-models

# 4. Create a Telegram bot:
#    - Talk to @BotFather, /newbot, grab the token
#    - Create a supergroup with Topics enabled
#    - Add the bot as admin

# 5. Start TRD
make setup TELEGRAM_BOT_TOKEN=123456:ABCDEF...
```

That's it. TRD is running in a tmux session. Your config is saved to the database — future starts need no env vars:

```bash
make start              # start (reads saved config)
make restart            # rebuild + restart after code changes
```

## Usage

In your Telegram supergroup, create a topic and send:

```
/start git@github.com:you/your-repo.git
```

Claude launches in that topic. Talk to it. Send voice messages. Everything flows through Telegram.

### Telegram commands

| Command | Effect |
|---------|--------|
| `/start <git-url>` | Clone repo, launch Claude |
| `/stop` | Kill session (mapping kept) |
| `/restart` | Resume previous conversation |
| `/reset` | Fresh conversation (clears history) |
| `/status` | Show tmux + channel state |
| `/watch` | See what Claude is doing (tmux capture) |
| `/debug` | Toggle debug mode |
| `/forget` | Delete the mapping |

### CLI commands

```bash
trd list               # all instances with state
trd watch <name>       # capture tmux pane
trd shell <name>       # open shell in repo
trd cd <name>          # print repo path
trd stop <name>        # kill tmux session
trd allow <user>       # add to allowlist
trd deny <user>        # remove from allowlist
trd allowed            # show allowlist
```

## Voice messages

Send a voice note in a topic — TRD transcribes it with embedded whisper and forwards the text to Claude. Claude can reply with voice too (`send_voice` tool).

```bash
make install-models     # downloads whisper + TTS models to ~/.trd/models/
```

Models are auto-detected at `~/.trd/models/`. No ffmpeg needed — Opus encode/decode is compiled into the binary.

## User allowlist

By default, anyone in the supergroup can use TRD. To restrict:

```bash
trd allow alice         # only @alice can interact
trd allow bob
trd allowed             # see the list
trd deny bob            # remove
```

Empty allowlist = everyone allowed.

## Day-to-day

```bash
tmux attach -t trd      # see dispatcher logs (Ctrl+B, D to detach)
make restart            # rebuild + restart after code changes
trd status              # check all instances
```

Claude instances survive dispatcher restarts — the channel plugin reconnects automatically. The rate-limit watchdog auto-dismisses Claude's rate-limit prompts and notifies you in the topic.

## Developing TRD itself

TRD can manage its own repo. Fork it, `/start` your fork in a topic, and have Claude modify TRD through Telegram:

```bash
make self-modify NAME=multi-claude-tg   # point channel plugin at instance checkout
make restart                             # rebuild with your changes
```

See [DEV.md](./DEV.md) for the full developer guide, debugging, and code layout.

## Documentation

| Doc | What's in it |
|-----|-------------|
| [ARCHITECTURE.md](./ARCHITECTURE.md) | System design, message flows, key decisions |
| [DEV.md](./DEV.md) | Contributing, code layout, debugging, env vars |
| [CLAUDE.md](./CLAUDE.md) | Key context for Claude Code when editing this repo |

## License

MIT — see [LICENSE](./LICENSE).
