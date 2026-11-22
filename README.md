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
| `CODEX_COMPACT_TIMEOUT` | `10m` | the deadline of one compaction, a Go duration such as `15m`; zero, a negative value or an unparseable value is refused. Used only by the stream mode: the oneshot mode (`CODEX_STREAM=false`, or `CODEX_KIND=oneshot`, and the Windows fallback `HERRSCHER_CODEX_ONESHOT_WINDOWS=1` even with `CODEX_STREAM=true`) validates it but never uses it, see [Compaction](#compaction) |
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

Both modes run headless with `sandbox_mode="workspace-write"` and the `neublox`
MCP auto-approved. An ungated session runs `approval_policy="never"`; a gated one
runs `on-request` and answers each approval (see [Approvals](#approvals)). Any
`--model` or `--effort` inside `CODEX_CMD` is lifted out of the argv and sent as a
turn parameter instead, because the Codex CLI rejects those flags before
`app-server`.

## Tool calls

A command execution is a tool call named `command`, its command in `Detail`. Its
`item/started` is a `tool` event carrying a `started` call whose `ID` is the
item id. Its `item/completed` is a `tool_done` event carrying the finished call
under the same `ID`, with `Tool` and `Detail` left empty on the event:

- `completed` with no exit code, or with exit code 0: `succeeded`.
- `completed` with a non-zero exit code, or `failed`: `failed`, with `Error` set
  to `exit code N`, or to `command failed` when no exit code came. An exit code
  counts only when it is an integer in the int32 range; any other number, such
  as `0.5` or `1e19`, counts as no exit code.
- `declined`: `failed`, with `Error` set to `command declined`.
- Any other status at the end, such as `inProgress`: the start only, with no
  finish.

A command whose start has an empty `command` text emits no start, so its finish
finds no open call and emits nothing. A finish closes the call its id opened,
whether or not its item still carries the command text.

`Error` never carries the command output. An item with no id emits its start as
a `tool` event without a `ToolCall`, and no finish. The protocol has no other
item type that this backend reports as a tool call. Every event it emits passes
`BackendEvent.ValidateToolCall`.

Each turn keeps two sets of ids, dropped when the turn ends, so nothing carries
over to the next turn or to a reset:

- the open calls, at most 256 at the same time;
- the seen ids, every id that has started in the turn, whether it opened a call,
  finished one or was refused by the open bound, at most 4096.

A start opens a call only when its id is not seen yet, the open calls are under
256, and the seen ids are under 4096. Any other start is emitted as a bare
`tool` event without a `ToolCall`, and it leaves the open calls untouched. So an
id finished earlier in the same turn never starts a call again: its repeated
start is bare, and its finish emits nothing. A start made while 256 calls are
open is bare too, and its id is seen, so it stays bare for the rest of the turn
even after a slot frees. Once 4096 ids are seen, every new start is bare. The
first start refused by each bound in a turn logs one warning, and a turn retried
after a reset is a new turn. An id longer than 256 bytes never opens a call: its
start is bare and its finish emits nothing. A finish is emitted only for an open
id, and it closes that id: it frees one of the 256 slots, but the id stays seen
for the rest of the turn. A repeated or unknown finish emits nothing. A call
still open when the turn ends keeps its start and never gets a finish.

Compaction items follow the same rule. A compaction's finish is always
`succeeded`, on its `item/completed`, because a `contextCompaction` item carries
no status: a failed compaction fails the turn, not the call.

## Approvals

A tool call that the agent wants to run is gated in one of two ways, and a turn
never runs an approval-gated tool without an answer.

- **The turn's asker.** When the turn's context carries a `contracts.ApprovalAsker`
  (`contracts.WithApprovalAsker`), each request is sent to the daemon as a
  `contracts.ApprovalRequest` with its own ID, the tool and the subject, and the
  answer with the same ID decides it. Only an `allow` runs the tool. A denial is
  declined. An error, an answer carrying another ID, or a turn that ends before
  the answer is also declined, and logged. The request carries no session name and no token: the
  daemon answers from its own machine, and the session token
  (`HERRSCHER_SESSION_TOKEN`) never takes this path.
- **The approval sub-process.** Without an asker, a session gated by the approval
  environment runs `herrscher approve ask --session ...`, which inherits the
  session token as before. Its answer decides the request.

A session is gated when its app-server starts on `on-request`, which happens when
the turn that starts it carries an asker or when the approval environment gates
it. A turn without an asker on a gated session that has no approval environment
declines every request.

- **A session started by an asker stays gated.** A session that an asker turn put
  on `on-request` refuses a later turn that carries no asker and no approval
  environment, with `errApprovalChannelLost`. It never runs such a turn ungated.
- **A session started ungated is relaunched.** A turn with an asker on a session
  started on `never` closes the codex process and relaunches it on `on-request`,
  resuming the same thread id, when that session already has a thread. Without a
  thread id (no turn has run yet) there is nothing to resume, so the turn is refused
  with `errUngatedSession`.
- **A rebuilt session is gated from its first turn.** A responder rebuilt from a
  resume id opens on `on-request` when the turn that opens it carries an asker. The
  herrscher-transport v0.7.0 or later server sets an asker on every remote turn; an
  older server sets none, and the session stays closed.
- **Compaction is not refused for lack of an asker.** `Compact` relaunches a `never` session the same way `Respond` does when its turn carries an asker, and refuses with `errUngatedSession` only when that session has no thread. It never applies `errApprovalChannelLost`: a session that an asker put on `on-request` keeps that policy, and a compaction without an asker runs on it. Any approval request that arrives during such a compaction is declined without calling any asker.
- **The oneshot path.** `codex exec` has no session to resume. A local oneshot run with an approval environment gate warns on stderr (`warnOneShotUngated`) and runs the command ungated. A remote run, whose turn carries an asker, is refused with `errUngatedSession` before the command runs.

Each request gets an id built from its method and the fields it carries (request
id, thread, turn, item), joined with `/` and with no empty segment, for example
`execCommandApproval/7` when only the request id is present. A request whose id is
null, or is neither a string nor a number, is declined with a logged error and the
asker is not called. An approval request without any id is not a server request
(it is a notification), so it never receives an answer and never reaches the
asker.

A request whose `threadId` is not the thread of the turn reading it, or that carries
no `threadId`, is declined with a logged error and the asker is not called. A
request whose `turnId` is not the id of the turn reading it is declined without calling
the asker, so no turn approves another turn's request. A request that arrives before
the reading turn has learned its own id (from the response to `turn/start`, from
`turn/started`, or from a compaction item) is parked, up to 16 requests. Once the id
is learned, a parked request naming that turn is served by the asker in arrival
order, and a parked request naming another turn is declined. A request beyond the
bound is declined at once. A turn that ends with requests still parked declines them
all. An approval request with no `turnId`, or with an empty one, is refused whatever
the session: the asker is not called, and the refusal is logged with a warning.
Warnings are deduplicated per session when the session has an approver, and per turn
otherwise. Other server requests without a turn id keep the handling they have on
master: they are not refused for lacking a turn id.

Unrecognised approval requests are declined and logged with a warning
(`unrecognised approval request`), deduplicated in the same way. They are accepted
only when the session is ungated and the turn has no asker: a nil approver with no
asker allows the request. Only a request on a `never` session can reach that path,
since an `on-request` session always has an approver or an asker.

The `neublox` MCP tools are auto-approved by `default_tools_approval_mode="approve"`
in the app-server arguments. They never reach the asker or the approval
sub-process, so the approval channel does not see them.

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

An `error` notification with `willRetry` false is the failure, and the reader
still waits for the `turn/completed` that follows it, so that completion never
leaks into the next turn. An `error` with `willRetry` true is ignored, because
the server retries it. An `item/started` of a `contextCompaction` item is
reported as a `tool` event naming the tool call `compaction`, and its
`item/completed` closes that call as `tool_done` with state `succeeded`. That
`succeeded` is deduced from `item/completed`: the schema carries no compaction
state, so the item does not say whether the compaction worked. A failure shows
in the turn error instead, which ends the turn with an error. The open-call rule
of the tool calls section applies to compactions too. Any other item is silent.

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

The `oneshot` mode has no session to compact, so its instance does not implement
the `Compact` port, although the manifest still declares
`Capabilities.Compactor`. The host therefore attempts a compaction, the instance
answers `no_compactor`, and the host refuses it: the first refusal logs one
warning for the daemon, then each compaction is refused without a new warning.
`CODEX_COMPACT_TIMEOUT` has no effect in that mode, which includes the Windows
fallback (`HERRSCHER_CODEX_ONESHOT_WINDOWS=1`) that serves oneshot even when
`CODEX_STREAM=true`.

## Model catalog

`Models` (`models.go`) is published through `Manifest.Models`, so the host can
read the catalog without instantiating the backend. Entries carry an id, a label,
the `--model` argument and the effort axis where there is one.

Most are route `native`, answered by the local `codex` login. A few `gw-*`
entries are route `gateway`, served by the gateway's OpenAI-shaped facade.

One rule decides every native value: it is in the catalog when a public OpenAI page
states it. The sources are OpenAI's models page
(https://developers.openai.com/api/docs/models), each model's page under
`/api/docs/models/<id>`, the pricing page (https://developers.openai.com/api/docs/pricing)
and the deprecations page (https://developers.openai.com/api/docs/deprecations),
read in November 2026. The model list cached by the local codex CLI is not a
source, so GPT-6 Astra and GPT-6.1 follow the same rule.

Visibility is a separate rule. A native model is visible in the host's selector
when the local `codex` login serves it, which is what `model/list` returns in
`discover.go`. Otherwise it is `Hidden`. A static entry that the login does not
serve would appear in the selector and fail when run, so it stays in the catalog
(its prices and window are kept and it still resolves by `ID`) but is hidden.
GPT-6 Astra, GPT-6.1 Sol and GPT-6.1 Terra are hidden for that reason: the login
did not list them in `model/list`. GPT-5.6 Sol is hidden for the same reason, since
the login does not serve it either. `TestRetiredUnservedAndSupersededModelsAreHidden`
pins all of them as hidden.

Each native entry carries its price in USD per million tokens (input, cached
input, 30-minute cache write, output) and its context window in tokens.

- The 30-minute cache write values are those of the short-context tier, 272K input
  tokens or fewer, which is the tier the catalog models.
- A context window is set only where a public page states it: 1.05M tokens for
  GPT-6 Astra, GPT-6 Luna, GPT-6.1 Sol and GPT-6.1 Terra, 400K for gpt-5-codex, and
  200K for o4-mini. GPT-5.5 and the GPT-5.6 models have no window on OpenAI's pages,
  so their window is 0, which means unknown. Cursor's "Up to 1M" is Cursor's own
  limit, not OpenAI's, and is not used here.
- The cache write is the 30-minute write, which is the only write price OpenAI
  lists for these models. GPT-5.5 has no write price on the pricing page, so its
  `CacheWritePrice` is 0.
- gpt-5-codex and o4-mini were shut down by OpenAI on 23 July 2026 and 23 October
  2026. Their model pages still state a price and a window beside the "Shut down"
  status, so those values are kept and both entries are `Hidden`.

Gateway entries (`gw-*`) carry no price. The gateway route bills at our price,
not OpenAI's (see the `InputPrice` contract on `ModelSpec`), and our gateway price
is not published in this catalog. Copying the native list price would show a
price that is not ours. For the same reason their window is 0: the native windows
of GPT-5.5 and GPT-5.6 are not published, so nothing is copied.

What the catalog does not model:

- The short-context tier. OpenAI's pricing is the standard tier up to 272K input
  tokens. A request above that pays a higher rate, which the contract cannot
  express, so the catalog understates the cost of long requests.
- The 12-hour cache write (OpenAI lists it at twice the 30-minute write for the
  GPT-6 and GPT-5.6 models). The contract has one cache write field, so only the
  30-minute write is stored.
- The GPT-5.6 Sol promotion. OpenAI states that its promotional pricing (the
  $4 input, $0.40 cached input, $20 output in the catalog) is available until
  30 November 2026. The page gives no price after that date, so the entry must be
  updated once OpenAI publishes it.
- The `none` reasoning effort of GPT-6.1 Terra. The page lists it next to low,
  medium, high, xhigh and max. The catalog keeps the five efforts shared with the
  other GPT-6 entries, so `none` cannot be selected through the host.

Retired, unserved and superseded models are `Hidden`: gpt-5-codex and o4-mini
(shut down), GPT-5.5 and GPT-5.6 Sol (absent from OpenAI's current model page),
GPT-6 Astra and GPT-6.1 Sol and Terra (not served by the local login, see the
visibility rule above), and the gateway duplicates `gw-gpt-5.5` and
`gw-gpt-5.6-sol`. They stay in the catalog so a resumed session still finds its
route by `ID`.

## Health and limits

The plugin registers `Probe` and `Limits`, the plugin level functions the host
calls for `herrscher health` and `herrscher usage` without a live backend. Both
run the `codex` binary named by the `cmd` setting (else `CODEX_CMD`, else
`codex`) under the environment of the account probed: `CODEX_HOME` is the value
`ProbeAccess.Getenv` gives, and a `CODEX_HOME` of the daemon that the access does
not name is dropped, so the native account never reads a profile directory and a
profile never reads the native one.

`Probe` runs `codex login status`, which spends no turn. Its exit code decides:

| Finding | State | Detail | Remedy |
|---|---|---|---|
| exit 0 | ok, with the latency of the check | `health.connected` | |
| any other exit | off | `health.not_logged_in` | run `codex login` for the native account, `herrscher account add codex <name>` for a profile |
| the binary is missing | off | `health.unreachable`, reason `codex not found` | |
| the host deadline passed | down | `health.unreachable`, reason `timeout` | |

With `Deep`, a logged in account also runs one minimal turn:
`codex exec --json --skip-git-repo-check --ephemeral --sandbox read-only` with
`approval_policy="never"`, the `model` setting (else `CODEX_MODEL`) when set,
and a one line prompt on stdin, from the temporary directory. A
`turn.completed` event is ok. A `turn.failed` event reads the limits: a window
used at 100% makes the state degraded with `health.rate_limited`, otherwise it
is down with the reason `turn failed`. No turn event at all is down with the
reason `no turn completed`.

`Limits` starts `codex app-server --listen stdio://` and sends
`account/rateLimits/read` with `excludeResetCreditDetails`, which reads the
ChatGPT plan usage the CLI itself shows and spends no turn. The method and its
response come from the protocol schema of `codex-cli` 0.167.0
(`GetAccountRateLimitsResponse`). Each bucket of `rateLimitsByLimitId` (else the
single `rateLimits` bucket) gives up to two windows, `primary` and `secondary`.
A window becomes a `contracts.Limit` when its `windowDurationMins` names a known
period: 300 minutes is five-hour, 10080 is weekly, 28 to 31 days is monthly. A
window of any other length is skipped. `usedPercent` is clamped to 0 to 100,
`resetsAt` (Unix seconds) becomes `ResetsAt` and the duration becomes `Span`.
The `codex` bucket is the whole account, so its scope is empty; any other
bucket is scoped by its stable `limitId` (`codex_spark`), never by its display
name. A refusal of the app-server (seen for an account that is not logged in;
an account on an API key is expected to be refused the same way) and a
response with no known window both return `contracts.ErrLimitsUnavailable`.
Failing to start the binary is an ordinary error.

The event stream of the app-server reports token usage per thread
(`thread/tokenUsage/updated`) without naming the model, so the backend fills no
`Models` breakdown.

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

## Windows process tree

On Windows the child tree is stopped with `taskkill /T /F`, a snapshot taken at kill
time. No Job Object is used, so a grandchild started after that snapshot, or one that
detaches before it, can outlive the backend. The Windows CI job runs the discovery and
kill tests only.

## Further reading

- [Herrscher docs](https://github.com/Herrscherd/herrscher-docs), page
  `plugins/backend`
- [contracts](https://github.com/Herrscherd/herrscher-contracts), for the port
  signatures
