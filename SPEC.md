# TRD — Telegram Repo Dispatcher

## What it is

A single Go binary that lets you talk to multiple OMC/Claude Code instances from one Telegram supergroup. Each topic = one repo. Create a topic, paste a repo URL, and you're coding.

## Architecture

```
Telegram Supergroup                    Go Binary (trd)
├── Topic: backend   ──┐          ┌── Telegram Bot (long-poll)
├── Topic: frontend  ──┼─────────▶├── HTTP/WS server (localhost:7777)
├── Topic: mobile    ──┤          ├── In-process pub/sub (map[id]chan)
└── /start <repo>    ──┘          ├── bbolt DB (~/.trd/state.db)
                                  └── Process manager (tmux)
                                           │
                        ┌──────────────────┼──────────────────┐
                    tmux: backend       tmux: frontend     tmux: mobile
                    claude --channels   claude --channels  claude --channels
                    (OMC + channel)     (OMC + channel)    (OMC + channel)
                        │                   │                  │
                    reads .trd/         reads .trd/        reads .trd/
                    config.json         config.json        config.json
                    (secret+port)       (secret+port)      (secret+port)
```

## Prerequisites

- Claude Code with channels enabled
- OMC plugin installed
- Git with SSH key (private repo access)
- Bun (channel plugins require it)
- tmux

Ensure there is an install script that checks all these dependencies and installs what is missing (and prompts the user how to configure them). This script should work on major Linux variants as well as MacOS and WSL. Not on normal Windows as that is too different to script well.

## Install

```
npm install -g telegram-repo-dispatcher
```

Ships platform-specific Go binary (darwin-arm64, linux-amd64, etc.) pulled at install time, plus the JS channel plugin shim.

## Usage

```
trd start --telegram-token <token> [--port 7777]
trd status
trd stop <topic-name>
trd logs <topic-name>
```

Run it in a tmux session yourself, or set up a user-level systemd service (`systemctl --user`).

## Identity & Persistence

This is the core design. Instances survive restarts, crashes, and reboots.

### First connection (onboarding)

1. User creates topic in supergroup, sends `/start git@github.com:org/repo.git`
2. TRD generates a stable **instance ID** (UUID) and a **secret** (random token)
3. Stores in bbolt: `topic_id ↔ instance_id ↔ repo_url ↔ repo_path ↔ secret`
4. `git clone` via SSH into `~/.trd/repos/<repo_url>/` (eg. `~/.trd/repos/github.com/pomofomo/multi-claude-tg/`)
5. Writes `<repo>/.trd/config.json`:
   ```json
   {
     "instance_id": "abc-123",
     "secret": "s3cr3t-token",
     "dispatcher_port": 7777
   }
   ```
6. Spawns tmux session: `tmux new-session -d -s trd-<instance_id> -c <repo_path> "claude --dangerously-load-development-channels server:trd-channel"`
7. Channel plugin starts, reads `.trd/config.json`, connects to dispatcher with secret
8. Dispatcher matches secret → links to topic. Ready.

### Reconnection (restart, crash, reboot)

1. Channel plugin reads same `.trd/config.json` → same secret
2. Connects to dispatcher, presents secret
3. Dispatcher looks up secret in bbolt → maps to correct topic
4. No re-registration needed. Ever.

### Health & auto-recovery

1. Dispatcher periodically checks if tmux session exists (`tmux has-session -t trd-<id>`)
2. If session is dead, tries to restart it
3. If restart fails 3 times, marks instance as `failed` in DB
4. Sends error message to the user in the Telegram topic: "Instance failed to start: <error>"
5. User can retry with `/restart` in the topic

## Components

### Go binary (`trd`)

| Subsystem | Implementation |
|-----------|----------------|
| Telegram | Long-polling, Bot API, reads `message_thread_id` for topic routing |
| HTTP/WS | `net/http` on localhost only. WebSocket for channel plugin connections |
| Pub/sub | `map[string]chan Message` — in-process, no external deps |
| Storage | bbolt — single file at `~/.trd/state.db` |
| Process mgr | `os/exec` + tmux — spawn, monitor, restart Claude sessions |

### Channel plugin (JS/Bun)

Thin MCP server shim. ~100 lines. Does three things:

1. Reads `.trd/config.json` for identity + dispatcher address
2. Opens WebSocket to `ws://localhost:<port>/channel/<instance_id>?secret=<secret>`
3. Bridges messages: WS ↔ Claude Code channel protocol (notifications in, reply tool out)

Distributed alongside the Go binary in the npm package.

## Message flow

```
User types in Topic:backend
    → Telegram Bot API delivers message with message_thread_id
    → TRD looks up thread_id in bbolt → instance_id: abc-123
    → TRD pushes message to Go channel for abc-123
    → WS sends to channel plugin in claude@backend
    → Channel plugin emits claude/channel notification
    → Claude/OMC processes, calls reply tool
    → Channel plugin POSTs reply over WS
    → TRD receives reply, sends to Telegram topic via message_thread_id
```

## Telegram commands

| Command | Effect |
|---------|--------|
| `/start <ssh-git-url>` | Clone repo, launch instance, bind to topic |
| `/stop` | Kill tmux session, mark inactive (keeps mapping) |
| `/restart` | Re-launch tmux session for this topic's repo |
| `/status` | Show instance state (running/stopped/failed) |

## File layout

```
~/.trd/
├── state.db                          # bbolt database
├── trd.log                           # dispatcher logs
└── repos/
    ├── <instance-id-1>/              # cloned repo
    │   ├── .trd/config.json          # identity file
    │   └── ...repo files...
    └── <instance-id-2>/
        ├── .trd/config.json
        └── ...repo files...
```

## npm package contents

```
telegram-repo-dispatcher/
├── bin/
│   ├── trd-darwin-arm64              # Go binary (macOS ARM)
│   ├── trd-darwin-amd64              # Go binary (macOS Intel)
│   ├── trd-linux-amd64               # Go binary (Linux)
│   └── trd-linux-arm64               # Go binary (Linux ARM)
├── channel/
│   └── index.ts                      # Channel plugin (Bun MCP server)
├── postinstall.js                    # Symlinks correct binary
└── package.json
```

## What exists (don't rebuild)

- Claude Code Channels protocol + `@modelcontextprotocol/sdk`
- OMC plugin (user installs separately)
- Telegram Bot API
- Go standard library
- bbolt (pure Go embedded KV store)

## Future considerations

Do not build these - they are listed for possible future directions, which may give some hints to how to architect the MVP for extensibility.

- Web dashboard for monitoring instances (exposed via localhost or Wireguard/Tailscale/Netbird VPN)
- Branch-aware topics (topic per branch, not just per repo) - leveraging git worktree
- Chat history persistence for context across Claude restarts
- Remote instances via SSH (launch tmux on remote machines)
