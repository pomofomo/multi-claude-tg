# Quick Start

Get TRD running in under 5 minutes.

## 1. Clone and build

```bash
git clone https://github.com/pomofomo/multi-claude-tg.git
cd multi-claude-tg
```

## 2. Check prerequisites

```bash
make install-deps         # interactive check — tells you what's missing
```

You need: `git`, `tmux`, `bun`, `claude` (Claude Code CLI), and Go 1.22+.

## 3. Create a Telegram bot

1. Open Telegram, talk to [@BotFather](https://t.me/BotFather).
2. Send `/newbot`, follow the prompts, copy the token.
3. Create a **supergroup** with **Topics** enabled.
4. Add the bot to the group and promote to **admin**.

## 4. Start TRD

```bash
make setup TELEGRAM_BOT_TOKEN=123456:ABCDEF...
```

This builds `trd`, installs the channel plugin, starts the dispatcher in
a tmux session, and **saves your config to the database**. You won't need
the token env var again.

## 5. Use it

In your Telegram supergroup, create a topic and send:

```
/start git@github.com:you/your-repo.git
```

Claude launches in that topic. Talk to it. Everything flows through Telegram.

## Day-to-day commands

```bash
tmux attach -t trd        # see dispatcher logs (Ctrl+B, D to detach)
make restart              # rebuild + restart after code changes
make start                # start trd (no env vars needed — reads from DB)
trd status                # see all instances
trd watch <name>          # see what Claude is doing
```

## Developing TRD through Telegram (self-modify)

TRD can manage its own repo — you develop it through Telegram, using the
very tool you're building. Here's how:

### Step 1: Fork and /start

1. Fork `pomofomo/multi-claude-tg` on GitHub (or use the original if you have push access).
2. In your Telegram supergroup, create a topic for TRD development.
3. Send `/start git@github.com:you/multi-claude-tg.git` in that topic.

Claude is now running in a checkout of TRD at `~/.trd/repos/<instance-id>/`.

### Step 2: Point TRD at the instance's channel plugin

The problem: when Claude edits `channel/index.ts` in its checkout, those
changes aren't reflected because `TRD_CHANNEL_ENTRY` still points to your
original source checkout. Fix this:

```bash
make self-modify NAME=multi-claude-tg
```

This finds the instance by repo name, updates `TRD_CHANNEL_ENTRY` to point
at the instance's `channel/index.ts`, saves it to the database, and restarts
the dispatcher.

### Step 3: Develop through Telegram

Now when Claude modifies TRD's code through Telegram:
- Go changes: tell Claude to run `make restart` — rebuilds the binary and
  restarts the dispatcher. Channel plugins reconnect automatically.
- Channel plugin changes: take effect on next dispatcher restart (the
  channel entry now points at the instance's checkout).

### The flow

```
You (Telegram) → TRD → Claude in topic
                          ↓
                  Edits TRD source code
                          ↓
                  Runs `make restart`
                          ↓
                  New binary, same Claude sessions
```

See [README.md](./README.md) for full documentation and [TODO.md](./TODO.md)
for the roadmap.
