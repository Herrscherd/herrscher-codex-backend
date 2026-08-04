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
| **Config & env** | `CODEX_CMD` (default: `codex`), `CODEX_MODEL`, `CODEX_EFFORT`, `CODEX_STREAM` (default: `true`), `CODEX_DIR`, `CODEX_KIND`; plus `HERRSCHER_CODEX_ONESHOT_WINDOWS=1` (Windows escape hatch); plus `env` — declared with no env binding and injected by the host per session (`K=V` per line, merged onto every spawned child), never read from the daemon's environment |
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

## Model catalog and the gateway route

`Models` (`models.go`) is published through `Manifest.Models`, so the host can
read the catalog without instantiating the backend. Entries carry an id, a
label, the `--model` argument and the effort axis where there is one. Most are
route `native` (the local `codex` login answers); a few `gw-*` entries are
route `gateway`, served by the gateway's OpenAI-shaped facade. Prices are left
at 0 (unknown).

Unlike the claude CLI, `codex` cannot be redirected with environment variables
alone: a third-party provider has to be declared in TOML. So when the host
injects `OPENAI_BASE_URL` through the `env` setting — the signal that this
spawn is on the gateway route — `NewBackend` materializes a **disposable,
per-spawn `CODEX_HOME`**: a `0700` temp directory holding a generated
`config.toml` that declares `[model_providers.neublox]` with
`wire_api = "responses"` (codex 0.146.0 rejects `"chat"` outright), and points
`CODEX_HOME` at it. The token is never written to that file — it is referenced
by `env_key` (`NEUBLOX_TOKEN`) and supplied at run time — so a directory left
behind on disk holds no secret. The responder owns the directory and removes it
on `Close`; construction failures remove it too, so a retried misconfigured
session does not leak one `/tmp/codex-home-*` per attempt. On the native route
nothing of this happens and no `CODEX_HOME` is set.

This quirk is deliberately confined to this plugin: neither the host, nor the
app, nor the claude backend knows about it.

## Development

This module sits outside the parent `go.work`, so local commands need
`GOWORK=off`; CI runs them without it.

```bash
GOWORK=off go test ./...
DCTL_LIVE=1 GOWORK=off go test -run Live ./...   # needs an authenticated codex
```

The host no longer derives model suggestions from this package's helpers: it
aggregates `Manifest.Models` across the compiled backends (`herrscher models
list`) and takes the choice as `session create --model`. Verify what a given
install actually recognizes with `codex debug models`.

## Further reading

- [Herrscher docs](https://github.com/Herrscherd/herrscher-docs) — `plugins/backend`
- [contracts](https://github.com/Herrscherd/herrscher-contracts) — port signatures
