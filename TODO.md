# TODO — next steps after MVP

Snapshot taken 2026-04-15 against commit `f24206c`. The dispatcher, channel plugin,
storage, tmux manager, CLI subcommands, health loop and npm packaging are all in.
What's below is the gap list between the code and SPEC.md / QUESTIONS.md, grouped
by how much each item matters to shipping a usable 0.1.

## P1 — correctness & reliability

- [x] **`.mcp.json` clobbers pre-existing repo config.** Fixed: `writeMCPConfig`
      now reads existing `.mcp.json` and merges the `trd-channel` entry.
- [x] **Dead `internal/pubsub` package.** Removed.
- [x] **Edited Telegram messages are dropped.** Fixed: `u.EditedMessage` is
      now forwarded with an "(edited)" prefix.
- [x] **Attachment directory grows without bound.** Fixed: `sweepAttachments`
      runs in the health loop, deleting files older than 7 days.
- [x] **No preflight for the `claude` binary.** Fixed: `exec.LookPath` check
      in `cmdStart` before cloning.
- [x] **Git URL is passed raw to `git clone`.** Fixed: `normalizeRepoURL`
      validates and converts HTTPS/bare URLs to SSH format, rejects flags.

## P1.5 — new features (from discussion 2026-04-16)

### CLI ergonomics: `trd shell`, `trd cd`, `trd list`

- [x] **Add `RepoName` to Instance struct.** Done in `d091fef`.
- [x] **`trd list`** — Done (alias for `trd status`).
- [x] **`trd shell <name>`** — Done.
- [x] **`trd cd <name>`** — Done.

### Voice messages → Whisper transcription

- [x] **Handle `m.Voice` and `m.Audio` in dispatcher.** Done in `d091fef`.
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

- [x] **README: "What installs where" section.** Done in `d091fef`.

## P2 — testing

- [x] **Dispatcher tests.** Added `normalizeRepoURL` table-driven tests.
- [x] **Storage edge-case tests.** Already had stale-index tests
      (`TestPutUpdatesStaleIndexes`); added `RepoNameFromURL` + `RepoName`
      round-trip tests.
- [x] **`ws` package tests.** Added auth rejection, serveConn reader/writer
      loop, and Conn.Send tests.
- [x] **Telegram client tests.** Added JSON round-trip tests for Voice, Audio,
      EditedMessage, Photo, GetMe response, SendMessageParams,
      EditMessageTextParams, and API error responses.

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
