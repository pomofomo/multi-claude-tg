# Manager Session Design

A manager session is a Claude instance that can orchestrate work across other TRD-managed repos. Instead of you manually switching between topics to coordinate, you talk to one "manager" topic and it delegates tasks to other instances, collects results, and reports progress back to you.

## Concept

```
You (Telegram)
  └── Topic: manager
        └── Claude (manager session)
              ├── delegates to: Topic: backend (via TRD API)
              ├── delegates to: Topic: frontend (via TRD API)
              └── delegates to: Topic: infra (via TRD API)
                    ↓
              collects results, synthesizes, reports back to you
```

Today, each Claude instance is isolated — it only sees messages in its own topic. A manager session breaks this isolation by giving Claude tools to:

1. **Send tasks to other instances** — "go implement X in the backend repo"
2. **Read responses from other instances** — poll or subscribe for their replies
3. **Report progress** — stream status updates to the manager topic as work proceeds

## How it could work

### Option A: Manager tools via the channel plugin

Add new MCP tools to the channel plugin that the manager Claude can call:

```
delegate(instance_name, message)     → sends a message to another instance's topic
poll(instance_name, since_msg_id)    → reads recent messages from another instance's topic
list_instances()                     → shows all running instances and their state
```

Under the hood, these tools talk to the dispatcher's existing API (`/api/instances`) and Telegram API (send message to another topic, read messages).

**Pros:** Minimal changes. Reuses existing infrastructure.
**Cons:** The manager reads/writes via Telegram, which means it sees Telegram-formatted messages (not raw Claude output). Latency of Telegram round-trip.

### Option B: Direct WS bridge between instances

The dispatcher creates a direct message channel between the manager instance and target instances, bypassing Telegram entirely:

```
Manager Claude → channel plugin → WS → dispatcher → WS → Target channel plugin → Target Claude
                                                      ↓
                                              (also posts to Telegram for user visibility)
```

New frame types: `delegate`, `delegate_result`, `delegate_status`.

**Pros:** Lower latency, richer data (not limited to Telegram formatting).
**Cons:** More complex. Target Claude needs to handle delegate frames differently from user messages.

### Option C: Dispatcher-level orchestration

The dispatcher itself becomes the orchestrator. The manager Claude sends a high-level task description, and the dispatcher manages the multi-instance coordination:

```
Manager Claude: "Deploy the new auth service: update backend, frontend, and infra"
  → Dispatcher breaks this into per-instance messages
  → Sends to each instance's topic
  → Collects replies
  → Forwards consolidated result to manager
```

**Pros:** Manager Claude doesn't need to manage coordination details.
**Cons:** Dispatcher becomes much more complex. Hard to handle nuanced multi-step tasks.

### Recommendation: Option A for v1

Start with Option A — it's the simplest and leverages everything we already have. The manager Claude gets tools to send messages to other topics and read their responses. It handles the orchestration logic itself (Claude is good at this). We post everything to Telegram so the user sees all progress.

## Proposed tools for the manager

### `delegate`

Send a task to another instance.

```json
{
  "name": "delegate",
  "arguments": {
    "instance": "backend",
    "message": "Add rate limiting to the /api/users endpoint",
    "wait": true
  }
}
```

- `instance`: repo name or instance ID prefix
- `message`: the task to send
- `wait`: if true, block until a response comes back (with timeout)
- Returns: the target instance's reply text

Under the hood: dispatcher sends a Telegram message to the target topic on behalf of the manager, waits for the reply, returns it.

### `broadcast`

Send the same message to multiple instances.

```json
{
  "name": "broadcast",
  "arguments": {
    "instances": ["backend", "frontend"],
    "message": "What's your current git branch and latest commit?"
  }
}
```

Returns: map of instance → response.

### `poll_instance`

Read recent activity from another instance's topic without sending a message.

```json
{
  "name": "poll_instance",
  "arguments": {
    "instance": "backend",
    "last_n": 5
  }
}
```

Returns: last N messages from that topic (both user and Claude messages).

### `list_instances`

Already exists as an API endpoint. Expose as a tool.

```json
{
  "name": "list_instances",
  "arguments": {}
}
```

Returns: all instances with name, state, tmux, channel status.

## Designating a manager session

Options:
1. **Any instance can manage** — all instances get the delegate tools. Simple but noisy.
2. **Explicit /manager command** — send `/manager` in a topic to promote it. Only manager instances get the extra tools.
3. **Dedicated manager instance** — a special instance with no repo, just orchestration tools.

Recommendation: Option 2. Any instance can be promoted to manager with `/manager`. The dispatcher adds the delegate tools to that instance's channel plugin capabilities. Multiple managers are fine.

## Progress visibility

When the manager delegates a task:
1. The task message appears in the target topic (user can see it)
2. The target Claude's replies appear in the target topic (user can see them)
3. The manager gets the reply back via the tool return value
4. The manager synthesizes and reports to its own topic

This means the user sees everything in Telegram — individual topic activity plus the manager's summary. No hidden work.

## Implementation sketch

### Phase 1: delegate + list_instances tools
- Add `delegate` tool to channel plugin
- Dispatcher handles `delegate` frame type:
  - Sends message to target instance's topic via Telegram
  - Waits for next Claude reply in that topic (subscribe to updates)
  - Returns reply text to the manager
- Add `list_instances` tool (calls existing API)

### Phase 2: poll + broadcast
- Add `poll_instance` tool (reads recent Telegram messages from a topic)
- Add `broadcast` tool (parallel delegate to multiple instances)

### Phase 3: Streaming progress
- Manager can subscribe to a target instance's replies in real-time
- Progress updates stream to the manager topic as they happen

## Open questions

1. **How does the manager wait for a response?** The delegate tool blocks until the target Claude replies. But what's the timeout? What if the target hits a rate limit?

2. **Should delegate messages look different in Telegram?** e.g., "🤖 Manager: Add rate limiting..." to distinguish from human messages.

3. **Can a manager delegate to itself?** Probably no — that's just normal conversation.

4. **What about task cancellation?** If the user sends `/stop` to a target while the manager is waiting for a response, the delegate tool should return an error.

5. **Authentication/authorization:** Should any instance be able to delegate to any other? Or should there be an explicit trust relationship?

6. **Repo-less manager:** Would you want a manager session that doesn't have its own repo checkout — just a pure orchestration point? Or always tied to a repo?
