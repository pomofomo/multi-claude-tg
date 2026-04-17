# Discord Integration Design

Design document for adding Discord as a second messaging platform alongside Telegram.

## Why Discord

Discord is the closest match to Telegram's forum model:
- **Forum channels** map 1:1 to Telegram forum topics (one thread per repo)
- **Voice messages** are receivable by bots (OGG/Opus with `IS_VOICE_MESSAGE` flag)
- **Gateway WebSocket** delivers events in real-time (analogous to Telegram long-poll)
- **Free API** with no per-request charges
- Excellent mobile app (iOS + Android)

## Concept mapping

| Telegram | Discord | Notes |
|----------|---------|-------|
| Supergroup | Guild (server) | One per TRD deployment |
| Forum topic | Forum channel thread | Created per repo |
| `message_thread_id` | `channel_id` (thread) | Thread is a channel in Discord |
| Bot token | Bot token | From Developer Portal |
| `getUpdates` (long-poll) | Gateway WebSocket | Persistent connection |
| `sendMessage` | `POST /channels/{id}/messages` | REST |
| `message.from.username` | `message.author.username` | User identification |
| Voice message (OGG) | Voice message (OGG, `IS_VOICE_MESSAGE` flag) | Same Opus codec |
| `/start <url>` | `!start <url>` or slash command | Discord uses `!` prefix or registered slash commands |

## Architecture

### Option A: Separate dispatcher (recommended for v1)

Run a second dispatcher binary (`trd-discord`) that shares the same bbolt DB, tmux sessions, and channel plugin. The channel plugin doesn't care where frames come from — it just bridges WS frames to Claude.

```
Discord Gateway ──> trd-discord ──> WS ──> channel plugin ──> Claude
Telegram Bot API -> trd          ──> WS ──> channel plugin ──> Claude
                                     │
                                  shared bbolt DB
                                  shared tmux sessions
```

Pros: No changes to existing Telegram code. Each dispatcher owns its platform.
Cons: Two processes, two configs. An instance can only be bound to one platform.

### Option B: Multi-platform dispatcher (future)

Single `trd` binary handles both Telegram and Discord. Each instance is bound to a (platform, channel_id) tuple. The dispatcher routes inbound messages from either platform to the correct instance.

```
Telegram Bot API ─┐
                  ├──> trd ──> WS ──> channel plugin ──> Claude
Discord Gateway  ─┘
```

Pros: Single process, unified state.
Cons: More complex code, platform-specific logic intermixed.

### Recommendation

Start with Option A. Extract the platform-agnostic core (bbolt, tmux, WS server, channel plugin) and build `trd-discord` as a thin wrapper that speaks Discord instead of Telegram. Once proven, merge into Option B.

## Discord Gateway integration

### Connection lifecycle

```go
// 1. Connect to Gateway
ws := connect("wss://gateway.discord.gg/?v=10&encoding=json")

// 2. Receive HELLO (opcode 10), start heartbeating
go heartbeatLoop(ws, hello.HeartbeatInterval)

// 3. Send IDENTIFY (opcode 2) with bot token + intents
ws.Send(Identify{Token: token, Intents: GUILD_MESSAGES | MESSAGE_CONTENT})

// 4. Receive READY — bot is online
// 5. Loop: receive DISPATCH events (opcode 0)
for event := range ws.Events() {
    switch event.Type {
    case "MESSAGE_CREATE":
        handleMessage(event.Data)
    case "THREAD_CREATE":
        handleThreadCreate(event.Data)
    }
}
```

### Required intents

```
GUILDS                 (1 << 0)   — guild/channel/thread events
GUILD_MESSAGES         (1 << 9)   — message events in guild channels
MESSAGE_CONTENT        (1 << 15)  — read message body (privileged, auto-approved <100 servers)
```

### Event flow: inbound message

```
User posts in forum thread
  → Gateway: MESSAGE_CREATE event
  → Parse: extract channel_id (thread), author, content, attachments
  → Lookup: channel_id → instance_id in bbolt
  → Build ws.Frame{type:"message", ...}
  → Push to channel plugin via WS
```

### Event flow: voice message

```
User sends voice note in thread
  → Gateway: MESSAGE_CREATE with flags & IS_VOICE_MESSAGE
  → Attachment: audio/ogg, Opus 48kHz
  → Download attachment via CDN URL
  → Decode OGG/Opus → PCM (existing audio package)
  → Whisper transcription (existing media.Engine)
  → Forward transcript as frame text
```

### Sending messages

```go
// Text reply
POST /channels/{channel_id}/messages
Body: {"content": "reply text"}

// File attachment
POST /channels/{channel_id}/messages (multipart/form-data)
Form: file=@path, payload_json={"content": "caption"}

// Voice message (bot sending)
POST /channels/{channel_id}/messages (multipart/form-data)
Form: file=@audio.ogg, payload_json={"flags": 8192}
// flags 8192 = IS_VOICE_MESSAGE — requires waveform field
```

## Forum channel setup

### One-time setup

1. Create a Discord server (guild).
2. Create a **Forum channel** (channel type 15, `GUILD_FORUM`).
3. Bot creates threads in the forum channel when `/start` is invoked.

### Creating a thread (on `/start`)

```go
POST /channels/{forum_channel_id}/threads
Body: {
    "name": "backend",           // repo name
    "auto_archive_duration": 10080, // 7 days
    "message": {
        "content": "Cloning git@github.com:org/backend.git..."
    }
}
```

Returns a channel object (the thread). Store `thread.id` as the instance's channel_id.

## bbolt schema changes

Add a `platform` field to Instance:

```go
type Instance struct {
    // ... existing fields ...
    Platform string `json:"platform"` // "telegram" or "discord"
}
```

The `by_topic` bucket key becomes `platform:chat_id:thread_id` to avoid collisions.

## Go libraries for Discord

| Library | Notes |
|---------|-------|
| [github.com/bwmarrin/discordgo](https://github.com/bwmarrin/discordgo) | Most popular, stable, supports Gateway + REST. ~5k stars. |
| [github.com/diamondburned/arikawa](https://github.com/diamondburned/arikawa) | More modern, type-safe, context-aware. Good for new projects. |
| Hand-rolled | Like TRD's Telegram client — minimal, only what's needed. Keeps deps small. |

Recommendation: Start with `discordgo` for speed. It handles Gateway reconnection, heartbeating, and event dispatch. Replace with hand-rolled later if the dependency is too heavy.

## Slash commands vs prefix commands

Discord supports registered slash commands (`/start`, `/stop`) which show up in autocomplete. However, they require registering commands via the API and handling interactions via a webhook or Gateway event. For v1, using message prefix commands (`!start <url>`) is simpler — no registration needed, same pattern as Telegram.

## Rate limits

Discord enforces rate limits per-route:
- Messages: 5 per 5 seconds per channel
- Global: 50 requests per second

The dispatcher should respect `X-RateLimit-*` response headers and back off. `discordgo` handles this automatically.

## Implementation plan

### Phase 1: Read-only proof of concept
- [ ] Connect to Discord Gateway, receive messages from a forum thread
- [ ] Map thread_id → instance in bbolt
- [ ] Forward messages to an existing Claude instance's channel plugin
- [ ] Receive Claude's replies and post them back to the thread

### Phase 2: Full lifecycle
- [ ] `/start` creates a forum thread, clones repo, launches Claude
- [ ] `/stop`, `/restart`, `/reset`, `/status`, `/watch` commands
- [ ] Voice message receive → whisper transcription
- [ ] TTS voice message send (if Discord's IS_VOICE_MESSAGE bot sending works reliably)
- [ ] User allowlist (shared with Telegram or per-platform)

### Phase 3: Unified dispatcher (Option B)
- [ ] Merge Discord + Telegram into single `trd` binary
- [ ] Platform-agnostic instance model
- [ ] Shared health loop, rate-limit watchdog

## Open questions

1. **Should an instance be accessible from both Telegram and Discord simultaneously?** Probably not for v1 — one binding per instance.
2. **Shared allowlist or per-platform?** Start shared (same bbolt bucket). Split later if needed.
3. **Discord slash commands vs prefix commands?** Start with prefix (`!start`), add slash commands in Phase 2.
4. **Should `trd-discord` reuse the same WS port?** Probably yes — the channel plugin doesn't know or care which platform the dispatcher is fronting.
