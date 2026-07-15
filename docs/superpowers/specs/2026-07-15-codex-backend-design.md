# Herrscher Codex Backend Design

## Goal

Create `github.com/Herrscherd/herrscher-codex-backend`, a standalone Go backend
that is the Codex equivalent of `herrscher-claude-backend`. It implements the
neutral `contracts.Backend` interface and self-registers as the `codex` backend
without changing the Claude backend.

## Architecture

The public surface mirrors the Claude backend:

- `Config` selects `stream` or `oneshot`, command, model, effort, working
  directory, and verbosity.
- `NewBackend` returns a `contracts.Backend`.
- `CommandPresets` exposes model/effort command suggestions.
- `init` registers a `contracts.Plugin` with manifest kind `codex` and
  `CODEX_*` settings.

The stream implementation uses Codex's local app-server over stdio. It starts
one `codex app-server --listen stdio://` process per backend session, performs
the JSON-RPC initialization handshake, starts one thread, and sends a
`turn/start` request for each prompt. Requests and notifications are newline-
delimited JSON objects; the wire omits the JSON-RPC 2.0 version field as Codex
documents.

The oneshot implementation runs `codex exec --json` once per prompt. It parses
the JSONL event stream and returns the final agent message, while exposing the
same `DCTL_*` environment variables and attachment/context behavior as Claude.

## Event mapping

Codex app-server and exec JSONL events are normalized to Herrscher events:

| Codex event | Backend event |
|---|---|
| `item/agentMessage/delta` or completed agent message | `text` |
| command execution, file change, MCP/tool item progress | `tool` |
| `turn/completed` | `result` |
| `turn/failed`, process EOF during a turn | error; emit `reset` for stream retry |

The final response is assembled from agent-message deltas when available, with
the completed agent-message item or final turn result as fallback. Unknown
notifications are ignored for forward compatibility. JSON lines are read with
`bufio.Reader.ReadBytes` rather than `bufio.Scanner` to support large payloads.

## Session and failure behavior

The stream responder serializes turns per session. It preserves the Codex thread
ID and resumes that thread after a process restart when the app-server supports
the resume request. A failed turn is returned as an error; a process failure
causes one restart/retry and emits `reset` before retrying, matching Claude's
consumer contract. `Close` closes stdin and reaps the child process.

The backend passes configured `Dir`, model, effort, and extra command arguments
to Codex. It does not embed API credentials or manipulate Codex authentication;
the installed CLI owns authentication.

## Context and attachments

Memory context is wrapped in the same data-only `<memory>` fence used by Claude,
with fence variants defanged before insertion. Image paths are appended as
plain local references. This keeps the neutral prompt behavior identical across
the two backends and lets Codex inspect local files from its working directory.

## Testing

Unit tests cover:

- backend kind selection and validation;
- app-server command construction and JSON-RPC request shapes;
- initialization, thread/turn request sequencing;
- parsing large JSONL lines and mapping text/tool/result events;
- attachment and memory-context formatting;
- process/session error handling and retry state;
- plugin self-registration and command presets.

An optional live two-turn test is gated behind `DCTL_LIVE=1` and requires a
locally authenticated `codex` executable. Standard `go build`, `go vet`, and
`go test` remain offline and must not consume model quota.

## Scope boundaries

This module does not implement an OpenAI API client, a Codex SDK dependency, a
remote app-server transport, or changes to Herrscher core. It relies only on the
published `herrscher-contracts` module and the local Codex CLI.
