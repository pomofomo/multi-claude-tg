# TODO — next steps after MVP

Snapshot taken 2026-04-15 against commit `f24206c`. The dispatcher, channel plugin,
storage, tmux manager, CLI subcommands, health loop and npm packaging are all in.
What's below is the gap list between the code and SPEC.md / QUESTIONS.md, grouped
by how much each item matters to shipping a usable 0.1.

## P1 — correctness & reliability

- [ ] **`.mcp.json` clobbers pre-existing repo config.** `dispatcher.writeMCPConfig`
      unconditionally writes `.mcp.json` at clone time. If the cloned repo
      already ships its own MCP config, we silently overwrite it. Merge the
      `trd-channel` entry into an existing file when present; only create
      fresh when absent.
- [ ] **Dead `internal/pubsub` package.** The dispatcher keeps its own
      `map[string]*liveConn`; `internal/pubsub` isn't imported anywhere but
      still has tests. Either adopt it (and delete the ad-hoc map) or remove
      the package so new contributors don't guess at which is canonical.
- [ ] **Edited Telegram messages are dropped.** `pollLoop` only dispatches
      `u.Message`; `u.EditedMessage` and `u.ChannelPost` fall on the floor.
      At minimum, forward edits to the bound instance as an `edit` frame (or
      a fresh message with an "(edited)" marker) so users who fix typos
      aren't ignored.
- [ ] **Attachment directory grows without bound.** `~/.trd/attachments`
      accumulates every downloaded file forever. Add a sweep in `healthLoop`
      that deletes files older than N days (or on `/forget`).
- [ ] **No preflight for the `claude` binary.** If `claude` isn't on `$PATH`,
      `launchTmux` succeeds (tmux starts) but the session dies immediately,
      and the health loop burns 3 restarts before reporting "failed." Do an
      `exec.LookPath(claudeBin)` in `cmdStart` and return a clear error to
      the topic up front.
- [ ] **Git URL is passed raw to `git clone`.** Any string following
      `/start ` becomes a `git clone` argument. Reject URLs that don't parse
      as `ssh://…`, `git@…:…`, or `https://…`, and refuse embedded flags
      (anything starting with `-`).

## P1.5 — new features (from discussion 2026-04-16)

### CLI ergonomics: `trd shell`, `trd cd`, `trd list`

- [ ] **Add `RepoName` to Instance struct.** Extract from the last path
      segment of the git URL (strip `.git`). Store at clone time. Use as
      the primary match target for all CLI subcommands (fall back to
      instance-ID prefix).
- [ ] **`trd list`** — human-friendly table of all instances: repo name,
      state, tmux alive, channel connected, short instance ID. Replaces
      the current `trd status` output (or alias it).
- [ ] **`trd shell <name>`** — `cd` into the repo path and `exec $SHELL`.
      Matches on repo name first, instance-ID prefix second.
- [ ] **`trd cd <name>`** — prints the repo path so the caller can
      `cd $(trd cd backend)`. Useful in scripts and shell aliases.

### Voice messages → Whisper transcription

- [ ] **Handle `m.Voice` and `m.Audio` in dispatcher.** Currently only
      `m.Document` and `m.Photo` are forwarded as attachments. Voice
      messages (`m.Voice`, OGG) and audio files (`m.Audio`) need the same
      treatment: download via `tg.DownloadFile`, set `attachment_file_id`.
- [ ] **Whisper transcription in the dispatcher.** When a voice/audio
      attachment arrives, run it through Whisper (CLI or OpenAI API) and
      send the transcript as the frame's `text`, with the original audio
      path as an attachment. Configure via `TRD_WHISPER_CMD` or
      `TRD_WHISPER_API_KEY` env var. If neither is set, forward the audio
      as-is (like photos today).

### Attachment flow improvements

- [ ] **Auto-download inbound photos.** Pre-fetch photos in the dispatcher
      and include a local `image_path` in the WS frame (mirroring the
      reference Telegram MCP plugin). Removes the two-step
      `download_attachment` dance Claude has to do today.
- [ ] **Use `sendPhoto` / `sendVoice` for outbound media.** Currently all
      outbound files go through `sendDocument`. Detect image/audio
      extensions and use the appropriate Telegram method for inline display
      and playback.

### Install documentation

- [ ] **README: "What installs where" section.** Clearly separate user-level
      (the `trd` binary, `~/.trd/`, systemd unit) from project-level
      (`.trd/config.json`, `.mcp.json` — auto-created by `/start`). Call
      out the channel plugin as user-level but invoked per-instance.

## P2 — testing

- [ ] **Dispatcher integration test.** Nothing exercises
      `handleMessage` → `routeToInstance` → WS round-trip → `OnOutbound`.
      Wire a fake `telegram.Client` and an in-memory ws pair and cover the
      happy path plus the "instance stopped" / "no channel connected"
      branches.
- [ ] **Storage edge-case tests.** `storage_test.go` covers put/get by each
      index but not stale-index cleanup when a row's `Secret` or
      `(ChatID, TopicID)` change mid-life. Add coverage.
- [ ] **`ws` package tests.** `serveConn`'s writer/reader loop and
      `handleChannel` auth rejection have no tests. A lightweight
      `net/http/httptest` harness against the coder-websocket client would
      cover both.
- [ ] **Telegram client tests.** `internal/telegram` is a hand-rolled HTTP
      wrapper and currently has zero tests. At least round-trip tests
      against an `httptest.Server` for the methods we actually call
      (`getMe`, `getUpdates`, `sendMessage`, `setMessageReaction`,
      `editMessageText`, `getFile`).

## P3 — UX / polish

- [ ] **Auto-download inbound images.** The channel plugin forwards
      `attachment_file_id` verbatim; Claude has to remember to call
      `download_attachment`. The reference Telegram MCP plugin pre-fetches
      photos and exposes them as `image_path`. Mirror that so image-heavy
      conversations don't rely on the model remembering a two-step dance.
- [ ] **Interim progress on `/start`.** Big repos clone in minutes; the
      topic stays silent. Use `edit_message` to update the "Cloning …"
      message with elapsed time or `git clone` stderr.
- [ ] **`/logs` as a Telegram command.** `trd logs <prefix>` exists on the
      CLI but there's no way to see the current tmux pane from inside the
      topic. Add a `/logs` handler that calls `tmuxmgr.CapturePane`,
      truncates, and replies.
- [ ] **Dismissing Claude's dev-channels confirmation is fragile.** The
      `sleep 3 ; tmux send-keys Enter` workaround (`launchTmux`) breaks
      silently if the prompt changes or the session is slow. Detect the
      prompt string with `tmux capture-pane` in a short loop instead of a
      fixed sleep.
- [ ] **`trd status` CLI can't see channel-connection state.** The
      dispatcher knows which instances have live WS connections; the CLI
      only knows tmux liveness. Expose a read-only admin endpoint
      (`GET /admin/status` on `127.0.0.1`) so the CLI can surface this.

## P4 — release & ops

- [ ] **CI / release automation.** `scripts/build-binaries.sh` and
      `postinstall.js` are ready, but there's no GitHub Action that tags →
      cross-builds → uploads binaries to a release → publishes to npm.
      Wire one up (`release-please` or a hand-rolled workflow).
- [ ] **Example systemd user unit.** README promises one "in a future
      release." Add `examples/trd.service` with `systemctl --user` usage.
- [ ] **Per-user allowlist.** SPEC defers this, but a minimal
      `TRD_ALLOWED_USERNAMES=alice,bob` env var would harden deployments
      where the supergroup isn't tightly controlled. Reject commands from
      non-allowlisted `message.from.username` with a polite error.
- [ ] **Document the `.mcp.json` 0644 choice.** `.trd/config.json` is
      `0600` (holds the secret); `.mcp.json` is `0644` because it's just a
      pointer. Add a note to QUESTIONS.md so reviewers don't flag it.

## P5 — explicitly deferred (see SPEC § Future considerations)

- Web dashboard for monitoring instances.
- Branch-aware topics via git worktrees.
- Chat history persistence across Claude restarts.
- Remote instances launched over SSH.

## Nice-to-fix drive-bys

- [ ] `cmd/trd/main.go` re-implements `strings.HasPrefix` as `startsWith`
      and a tiny `stringReader` instead of `strings.NewReader`. Replace
      with stdlib helpers.
- [ ] `dispatcher.preview` / `truncate` / `shortID` could move to a
      tiny `internal/logutil` so `cmd/trd` can reuse them.
- [ ] `writeMCPConfig` builds JSON with `fmt.Sprintf` + `json.Marshal` for
      the args array; switch to a single `json.MarshalIndent` of a struct
      for clarity.
