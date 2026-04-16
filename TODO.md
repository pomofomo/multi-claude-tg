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
- [x] **Whisper transcription in the dispatcher.** Done: `internal/media`
      package handles both CLI (`TRD_WHISPER_CMD`) and OpenAI API
      (`TRD_OPENAI_API_KEY`) backends. Gracefully degrades when unconfigured.
- [x] **Clean up whisper sidecar files.** Done: `transcribeAttachment`
      removes `.txt`/`.vtt`/`.srt`/`.json`/`.tsv` artifacts after transcription.

### Attachment flow improvements

- [ ] **Auto-download inbound photos.** Pre-fetch photos in the dispatcher
      and include a local `image_path` in the WS frame (mirroring the
      reference Telegram MCP plugin). Removes the two-step
      `download_attachment` dance Claude has to do today.
- [x] **Use `sendPhoto` / `sendVoice` for outbound media.** Done:
      dispatcher detects file extension and routes to `SendPhoto`,
      `SendVoice`, or `SendAudio` accordingly.

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
- [x] **Interim progress on `/start`.** Done: clone message is edited
      every 10s with elapsed time while `git clone` runs.
- [x] **`/watch` Telegram command (was `/logs`).** Done: `/watch` in a
      topic calls `tmuxmgr.CapturePane` and replies with the pane content.
      CLI renamed from `trd logs` to `trd watch` (`logs` kept as alias).
- [x] **Dismissing Claude's dev-channels confirmation is fragile.** Done:
      replaced `sleep+send-keys` shell hack with `autoConfirm` goroutine
      that polls `capture-pane` for a prompt (up to 30s, 500ms interval).
- [x] **`trd status` CLI shows channel-connection state.** Done:
      `/api/instances` now returns `connected` and `tmux_alive` fields.
      CLI `trd status` displays `channel=true/false`.

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

- [x] `cmd/trd/main.go` stdlib cleanup. Done: removed `dirOf` (replaced
      with `filepath.Dir`), inlined `asReader` as `strings.NewReader`.
- [ ] `dispatcher.preview` / `truncate` / `shortID` could move to a
      tiny `internal/logutil` so `cmd/trd` can reuse them. (No actual
      duplication exists today — deferred until needed.)
- [x] `writeMCPConfig` uses `json.MarshalIndent` of a struct. Already
      done in a prior commit.
