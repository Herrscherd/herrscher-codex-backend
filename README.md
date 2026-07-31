# herrscher-codex-backend

**Codex CLI as a Herrscher backend.** Turns an inbound prompt into a reply by
driving the locally installed `codex` binary — either a persistent
`codex app-server` process speaking JSONL, or a fresh `codex exec --json` per
message. It is not an API client: it holds no key and reaches no OpenAI endpoint
itself; authentication belongs to your Codex install.

## Role · Category · Ports · Config · Status · Repo

| Aspect | Value |
|--------|-------|
| **Role** | Answers one prompt per turn by driving the local Codex CLI. |
| **Category** | Backend (model edge) |
| **Ports implemented** | `contracts.Backend`; `contracts.ResumeAware` (stream mode only) |
| **Config & env** | `CODEX_CMD` (default: `codex`), `CODEX_MODEL`, `CODEX_EFFORT`, `CODEX_STREAM` (default: `true`), `CODEX_DIR`, `CODEX_KIND`; plus `HERRSCHER_CODEX_ONESHOT_WINDOWS=1` (Windows escape hatch) |
| **Status** | live |
| **Repo** | [herrscher-codex-backend](https://github.com/Herrscherd/herrscher-codex-backend) |

## Install

```bash
herrscher plugin add github.com/Herrscherd/herrscher-codex-backend
```

## Two modes

`stream` (default) starts `codex app-server --listen stdio://` once and sends one
`turn/start` per message, mapping `item/agentMessage/delta`, command executions
and `turn/completed` onto `contracts.BackendEvent`. A dead process emits a
`reset` event, then restarts and retries the turn against the same thread id.
`ResumeToken()` exposes that thread id; the host persists it and feeds it back
through the `resume` setting at construction (host-injected — it is not one of
the declared, env-backed settings).

`oneshot` (`CODEX_STREAM=false` or `CODEX_KIND=oneshot`) runs `codex exec --json`
per message and additionally exports `DCTL_MSG`, `DCTL_AUTHOR`,
`DCTL_MESSAGE_ID`, `DCTL_CHANNEL` and `DCTL_ATTACHMENTS` to the child.

Both modes run headless: `approval_policy="never"`,
`sandbox_mode="workspace-write"`, and the `neublox` MCP auto-approved. Any
`--model` / `--effort` inside `CODEX_CMD` is lifted out of the argv and sent as a
turn parameter instead, because the Codex CLI rejects those flags before
`app-server`.

## Development

This module sits outside the parent `go.work`, so local commands need
`GOWORK=off`; CI runs them without it.

```bash
GOWORK=off go test ./...
DCTL_LIVE=1 GOWORK=off go test -run Live ./...   # needs an authenticated codex
```

`CommandPresets("codex")` returns the model × effort matrix used for
`/session create cmd:` suggestions. Verify a given install with
`codex debug models`.

## Further reading

- [Herrscher docs](https://github.com/Herrscherd/herrscher-docs) — `plugins/backend`
- [contracts](https://github.com/Herrscherd/herrscher-contracts) — port signatures
