# Herrscher Codex Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone Go backend that provides the Claude backend's Herrscher contract through Codex app-server and `codex exec`.

**Architecture:** Keep the Claude backend's public shape and neutral prompt helpers, but implement Codex's native JSONL app-server protocol for persistent sessions. Use `codex exec --json` for one-shot turns, with shared event parsing and context/attachment formatting.

**Tech Stack:** Go 1.25, `github.com/Herrscherd/herrscher-contracts v0.1.4`, local Codex CLI, newline-delimited JSON.

## Global Constraints

- The module is `github.com/Herrscherd/herrscher-codex-backend`.
- The plugin manifest kind is `codex`; settings use `CODEX_*` environment names.
- The only Go dependency is the published `herrscher-contracts` module.
- Standard tests are offline; live Codex tests require `DCTL_LIVE=1`.
- No changes are made to `herrscher-claude-backend` or Herrscher core.
- Production code follows test-first red-green-refactor cycles.

## File Map

- `go.mod`: module metadata and contracts dependency.
- `backend.go`: `Config`, backend construction, one-shot execution, presets.
- `stream.go`: app-server JSONL protocol, session lifecycle, stream responder,
  shared context/attachment helpers.
- `register.go`: self-registration as the `codex` backend.
- `backend_test.go`: selection, one-shot environment/arguments, presets.
- `stream_test.go`: JSON-RPC shapes, parser, session sequencing, helpers.
- `register_test.go`: registry contract.
- `stream_live_test.go`: optional authenticated two-turn smoke test.
- `README.md`: usage, protocol, configuration, and verification commands.

### Task 1: Scaffold and backend contract

**Files:** Create `go.mod`, `backend.go`, `backend_test.go`.

**Interfaces:** Produce `Config`, `NewBackend(context.Context, Config)`,
`resolveBackend`, `oneShotResponder`, `runCmd`, and `CommandPresets`.

- [ ] Write failing tests for explicit/default kind selection, empty oneshot
  command rejection, command preset labels/values, and `runCmd` passing the
  prompt as both stdin and final argument with `DCTL_*` environment variables.
- [ ] Run `go test ./...`; verify failures identify missing backend symbols.
- [ ] Implement the smallest contract-compatible backend shell and one-shot
  responder. Use `codex exec --json` as the default one-shot command suffix,
  append the prompt as the final argument, and expose `DCTL_MSG`,
  `DCTL_AUTHOR`, `DCTL_MESSAGE_ID`, `DCTL_CHANNEL`, and `DCTL_ATTACHMENTS`.
- [ ] Run `go test ./...`; verify the task tests pass.
- [ ] Commit with `feat: scaffold Codex backend contract`.

### Task 2: App-server request and event protocol

**Files:** Create `stream.go`, `stream_test.go`.

**Interfaces:** Produce `appServerArgv`, `initializeRequest`,
`threadStartRequest`, `turnStartRequest`, `readAppEvent`, `readTurn`, and
`appEvent`/`turnResult` types used by the session.

- [ ] Write failing tests for the exact app-server command, initialize and
  initialized handshake, `thread/start`, and `turn/start` request JSON. Test
  parsing `item/agentMessage/delta`, completed agent messages, command items,
  `turn/completed`, `turn/failed`, unknown notifications, and a 200 KB JSONL
  line.
- [ ] Run `go test ./...`; verify protocol tests fail before implementation.
- [ ] Implement newline-delimited JSON encoding/decoding with request IDs,
  response matching, thread ID extraction, final text assembly, and
  `contracts.BackendEvent` mapping. Use `bufio.Reader.ReadBytes('\n')`.
- [ ] Run the focused stream tests, then `go test ./...`; verify all pass.
- [ ] Commit with `feat: implement Codex app-server protocol`.

### Task 3: Persistent session and retry behavior

**Files:** Modify `stream.go`; extend `stream_test.go`; create
`stream_live_test.go`.

**Interfaces:** Produce `newAppSession`, `startAppSession`, `(*appSession).Send`,
`(*appSession).Close`, and `streamResponder` implementing
`contracts.Backend`.

- [ ] Write failing tests using `io.Pipe` for one initialized session handling
  two sequential prompts on one thread, serialized sends, process EOF emitting
  `reset`, one restart with the prior thread ID, and terminal turn errors.
- [ ] Run the focused tests and verify the expected failures.
- [ ] Implement process startup with `codex app-server --listen stdio://`,
  handshake/thread setup, mutex-serialized sends, thread resume, one retry,
  `reset` emission, and child process reaping on `Close`.
- [ ] Add a live two-turn test gated by `DCTL_LIVE=1`; it must skip otherwise.
- [ ] Run focused tests and `go test ./...`; verify the offline suite passes.
- [ ] Commit with `feat: add persistent Codex backend sessions`.

### Task 4: Shared prompt behavior and plugin registration

**Files:** Modify `backend.go` and `stream.go`; create `register.go` and
`register_test.go`; extend tests.

**Interfaces:** Produce shared `withAttachments`, `withContext`, and the
`contracts.Plugin` registration with `Kind: "codex"`.

- [ ] Write failing tests for attachment formatting, memory-fence defanging,
  registration, and mapping `CODEX_CMD`, `CODEX_MODEL`, `CODEX_EFFORT`,
  `CODEX_STREAM`, `CODEX_DIR`, and `CODEX_KIND` into `Config`.
- [ ] Run the tests and verify they fail for the missing registration/helpers.
- [ ] Implement helpers identical in behavior to Claude's neutral prompt
  contract, then register the plugin and wire stream/oneshot settings.
- [ ] Run `go test ./...` and `go vet ./...`; verify clean output.
- [ ] Commit with `feat: register Codex backend plugin`.

### Task 5: Documentation and final verification

**Files:** Create `README.md`; modify no other repositories.

- [ ] Document installation, `NewBackend`, stream and oneshot commands,
  app-server event mapping, configuration, presets, attachments, and live test.
- [ ] Run `gofmt -w *.go`, `go build ./...`, `go vet ./...`, and `go test ./...`.
- [ ] Inspect `git diff --check` and `git status --short`; verify only intended
  module files are present.
- [ ] Commit with `docs: document Codex backend`.
