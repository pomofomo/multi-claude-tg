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

## Developing TRD itself

TRD manages its own development nicely. You can `/start` this repo in a
topic and have Claude modify TRD through Telegram. After changes:

```bash
make restart              # rebuilds, restarts dispatcher, Claude instances reconnect
```

See [README.md](./README.md) for full documentation and [TODO.md](./TODO.md)
for the roadmap.
