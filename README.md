# herrscher-codex-backend

**Codex CLI as a Herrscher backend.** It turns an inbound prompt into a reply by
driving the locally installed `codex` binary: either a persistent
`codex app-server` process speaking JSONL, or a fresh `codex exec --json` per
message.

It is not an API client. It holds no key and reaches no OpenAI endpoint itself.
Authentication belongs to your Codex install.

Category: backend, the model edge. Ports: `contracts.Backend`, and
`contracts.ResumeAware` in stream mode. Status: live.

## Install

```bash
herrscher plugin add github.com/Herrscherd/herrscher-codex-backend
```

## Configuration

| Setting | Default | What it is |
|---|---|---|
| `CODEX_CMD` | `codex` | the binary to run |
| `CODEX_MODEL` | | the model a session gets when it names none |
| `CODEX_EFFORT` | | the reasoning effort |
| `CODEX_STREAM` | `true` | `false` selects the oneshot mode |
| `CODEX_DIR` | | the working directory the CLI runs in |
| `CODEX_KIND` | | `stream` or `oneshot`, the explicit form of `CODEX_STREAM` |
| `CODEX_COMPACT_TIMEOUT` | `10m` | the deadline of one compaction, a Go duration such as `15m`; zero, a negative value or an unparseable value is refused. Without effect in oneshot mode (`CODEX_STREAM=false`), which has no compaction |
| `HERRSCHER_CODEX_ONESHOT_WINDOWS` | | set to `1` as a Windows escape hatch |

There is one more setting, `env`, and it has no environment binding on purpose.
The host injects it per session, `K=V` per line, merged onto every spawned child.
It is never read from the daemon's own environment.

## Two modes

`stream`, the default, starts `codex app-server --listen stdio://` once and sends
one `turn/start` per message, mapping `item/agentMessage/delta`, command
executions and `turn/completed` onto `contracts.BackendEvent`. A dead process
emits a `reset` event, then restarts and retries the turn against the same thread
id.

`ResumeToken()` exposes that thread id. The host persists it and feeds it back
through the `resume` setting at construction, which is host-injected rather than
one of the declared, env-backed settings.

`oneshot` (`CODEX_STREAM=false` or `CODEX_KIND=oneshot`) runs `codex exec --json`
per message, and additionally exports `DCTL_MSG`, `DCTL_AUTHOR`,
`DCTL_MESSAGE_ID`, `DCTL_CHANNEL` and `DCTL_ATTACHMENTS` to the child.

Both modes run headless: `approval_policy="never"`,
`sandbox_mode="workspace-write"`, and the `neublox` MCP auto-approved. Any
`--model` or `--effort` inside `CODEX_CMD` is lifted out of the argv and sent as a
turn parameter instead, because the Codex CLI rejects those flags before
`app-server`.

## Compaction

The stream mode implements `contracts.Compactor`, and its manifest declares
`Capabilities.Compactor`. `Compact` sends the app-server method
`thread/compact/start` with the thread id of the session, and it holds the same
turn lock as `Respond`, so it never runs alongside a turn.

The method and its params come from the protocol schema of `codex-cli` 0.161.0
(`ThreadCompactStartParams`, whose only field is `threadId`). The compaction is
read by the same turn reader as a turn. A turn's id comes from the response to
`turn/start` (`TurnStartResponse.turn.id`, required by the schema), or from its
`turn/started` notification, which the schema does not guarantee. The reader
takes a `turn/completed` only when it closes the turn whose id it knows. A
completion that names another turn is ignored. A completion that arrives before
the id is known is kept in a queue of at most 16 entries and applied as soon as
the id is learned. A seventeenth completion that arrives before the id is known
does not displace the others: the turn fails at once with `errParkedFull`, which
names the turn that overflowed, and the session is dropped. No completion is
discarded silently. If the response to `turn/start` and a `turn/started` name two
different ids for one turn, the turn fails at once with `errTurnIDMismatch`,
which names both ids, and the session is dropped, because the server may still
run the turn that was not announced.

A compaction's response is empty (`ThreadCompactStartResponse`), so its id comes
from the `turnId` of its `item/started` notification for a `contextCompaction`
item (required by the schema), or from its `turn/started`. The turn lock does
not identify the compaction: a completion of another turn of the thread closes
the compaction only if it carries that id. A compaction whose id never arrives
waits for the context of its caller or its ten-minute deadline.

An `error` notification with `willRetry` false is the failure,
and the reader still waits for the `turn/completed` that follows it, so that
completion never leaks into the next turn. An `error` with `willRetry` true is
ignored, because the server retries it. Its start, an `item/started` of a
`contextCompaction` item, is reported as a `tool` event named `compaction`; any
other item is silent.

The reader stops only on the `turn/completed` of its turn or on the context of
its caller. An `error` with `willRetry` false followed by no `turn/completed`
leaves it waiting until that context ends: `Respond` has no deadline of its own,
so the caller must cancel. `Compact` adds its own deadline, ten minutes unless
`CODEX_COMPACT_TIMEOUT` sets another Go duration.

With no approver configured, a server request received during a compaction
(an approval, or any unrecognised request) is accepted, as `Respond` accepts it.

A free instruction is refused, not dropped, and the decision is final for this
backend: `thread/compact/start` has no field for an instruction, so codex cannot
honour one. A non-blank `CompactRequest.Instruction` returns an error before
anything is sent to the app-server.

A compaction needs a thread. With no open session and no thread to resume, it
refuses with an error instead of starting an empty thread to compact.

The result never claims a size. `thread/tokenUsage/updated` reports the usage of
the last turn and the running total, not the size of the context after the
compaction, so `TokensMeasured` stays false and the note says the size was not
measured.

A failed compaction is not sent again, unlike a turn: a compaction is never run
twice. A compaction that is cancelled, reaches its deadline, or loses the
app-server drops the session, and the next compaction reopens the same thread.
A compaction refused or failed by codex keeps the session.

When a turn or a compaction is abandoned, the thread is reopened through
`resumeID`, and the items that the abandoned turn already produced stay in the
thread history that the reopened session resumes. The reader cannot remove them,
so the next turn may read them as context. This limit is not verified against a
live app-server.

The `oneshot` mode has no session to compact, so it does not implement the port.

## Model catalog

`Models` (`models.go`) is published through `Manifest.Models`, so the host can
read the catalog without instantiating the backend. Entries carry an id, a label,
the `--model` argument and the effort axis where there is one.

Most are route `native`, answered by the local `codex` login. A few `gw-*`
entries are route `gateway`, served by the gateway's OpenAI-shaped facade. Prices
are left at 0, meaning unknown.

## The gateway route needs a config file

Unlike the claude CLI, `codex` cannot be redirected with environment variables
alone: a third-party provider has to be declared in TOML.

So when the host injects `OPENAI_BASE_URL` through the `env` setting, which is
the signal that this spawn is on the gateway route, `NewBackend` materializes a
**disposable, per-spawn `CODEX_HOME`**. It is a `0700` temp directory holding a
generated `config.toml` that declares `[model_providers.neublox]` with
`wire_api = "responses"`, since codex 0.146.0 rejects `"chat"` outright, and
`CODEX_HOME` is pointed at it.

The token is never written to that file. It is referenced by `env_key`
(`NEUBLOX_TOKEN`) and supplied at run time, so a directory left behind on disk
holds no secret.

The responder owns the directory and removes it on `Close`. Construction failures
remove it too, so a retried misconfigured session does not leak one
`/tmp/codex-home-*` per attempt. On the native route none of this happens and no
`CODEX_HOME` is set.

This quirk is deliberately confined to this plugin. Neither the host, nor the
app, nor the claude backend knows about it.

## Development

This module sits outside the parent `go.work`, so local commands need
`GOWORK=off`. CI runs them without it.

```bash
GOWORK=off go test ./...
DCTL_LIVE=1 GOWORK=off go test -run Live ./...   # needs an authenticated codex
```

The host aggregates `Manifest.Models` across the compiled backends
(`herrscher models list`) and takes the choice as `session create --model`.
Verify what a given install actually recognizes with `codex debug models`.

## Further reading

- [Herrscher docs](https://github.com/Herrscherd/herrscher-docs), page
  `plugins/backend`
- [contracts](https://github.com/Herrscherd/herrscher-contracts), for the port
  signatures
