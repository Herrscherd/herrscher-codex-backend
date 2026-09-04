package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/Herrscherd/herrscher-contracts"
)

// approver answers the approval requests codex sends over the app-server
// protocol by asking the daemon that started this session. It exists only when
// the environment carries a session, a mode and a binary to ask with: without
// all three there is nobody to ask, and the turn runs as it did before
// approvals existed.
type approver struct {
	session, bin string
	warn         io.Writer
	warnMu       sync.Mutex
	warned       map[string]bool
	// ask runs one argv and returns its stdout. Injected so a test can answer
	// without a daemon and without a binary on disk.
	ask func(ctx context.Context, bin string, argv []string) ([]byte, error)
}

// newApprover reads the spawn environment the supervisor wrote. It returns nil
// when nothing there says a policy is in force, which is the whole of the
// decision: this backend never infers a policy from anything else.
func newApprover(look func(string) string) *approver {
	session, _, bin, gated := contracts.ApprovalsFromEnv(look)
	if !gated {
		return nil
	}
	return &approver{session: session, bin: bin, ask: runApprove, warn: os.Stderr}
}

func runApprove(ctx context.Context, bin string, argv []string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, argv...).Output()
}

// allow asks the daemon about one tool call. Every failure allows, exactly as
// the Claude Code hook does: a daemon that cannot be reached, an answer that
// cannot be read, a binary that is not there. This is a guardrail against
// mistakes, not a sandbox, and a session that denied everything the moment the
// daemon restarted would be worse than one that never gated at all.
//
// ctx carries no deadline of its own: `approve ask` legitimately blocks for as
// long as the daemon's approval wait while a human decides, and giving up first
// would allow every call the operator was too slow to answer.
func (a *approver) allow(ctx context.Context, tool, subject string) bool {
	if a == nil {
		return true
	}
	out, err := a.ask(ctx, a.bin, []string{
		"approve", "ask",
		"--session", a.session,
		"--tool", tool,
		"--subject", subject,
	})
	if err != nil {
		return true
	}
	var verdict struct {
		Decision string `json:"decision"`
	}
	if json.Unmarshal(out, &verdict) != nil || verdict.Decision == "" {
		return true
	}
	return verdict.Decision == "allow"
}

// approvalSubject maps one app-server approval request onto the tool name and
// subject a herrscher rule is written against. The two spellings per kind are
// the current method names and the legacy ones codex still emits: answering
// only one of them would hang the turn on the other until its deadline.
//
// A method nobody here recognises returns ok false. Its caller answers it
// anyway, because a server request left without a response is what actually
// stalls a turn.
func approvalSubject(method string, params map[string]any) (tool, subject string, ok bool) {
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		cmd, _ := params["command"].(string)
		return "Bash", cmd, true
	case "item/fileChange/requestApproval", "applyPatchApproval":
		changes, _ := params["fileChanges"].(map[string]any)
		paths := make([]string, 0, len(changes))
		for p := range changes {
			paths = append(paths, p)
		}
		// Sorted because a Go map iterates in a random order, and a subject that
		// changed between two identical requests could not be matched by a rule,
		// nor recognised by an operator reading the same call twice.
		sort.Strings(paths)
		return "Write", strings.Join(paths, " "), true
	}
	return "", "", false
}

// approvalResponse renders the reply codex expects on the request's own id.
//
// "decline" and never "cancel": cancel aborts the whole turn, so a single
// refused command would end the session's work instead of letting the agent
// pick another way. A denied call is a denied call, not an interrupt.
func approvalResponse(id any, allow bool) map[string]any {
	decision := "decline"
	if allow {
		decision = "accept"
	}
	return map[string]any{"id": id, "result": map[string]any{"decision": decision}}
}

// approvalPolicy is the value codex is started with. Under a policy it must ask
// rather than decide alone, which is what turns the requests above into
// something that ever arrives; without one it keeps the headless behaviour it
// has always had, because there is no channel to answer on.
//
// sandbox_mode is deliberately not derived from the rules. It bounds where the
// agent can write at all, which is a property of the worktree, not of what an
// operator happened to allow.
func approvalPolicy(gated bool) string {
	if gated {
		return "on-request"
	}
	return "never"
}

// answerApproval replies to one server request. Every request gets a response
// on its own id, including one whose method nothing here recognises: a request
// left unanswered stalls the turn until its deadline, which costs an operator
// far more than an unrecognised call allowed.
func answerApproval(ctx context.Context, msg map[string]any, id any, ap *approver, respond func(map[string]any) error) {
	method, _ := msg["method"].(string)
	params, _ := msg["params"].(map[string]any)
	tool, subject, known := approvalSubject(method, params)
	if !known {
		ap.warnUnrecognisedApproval(method)
		_ = respond(approvalResponse(id, true))
		return
	}
	_ = respond(approvalResponse(id, ap.allow(ctx, tool, subject)))
}

func looksLikeApprovalRequest(method string) bool {
	return strings.HasSuffix(method, "requestApproval") || strings.HasSuffix(method, "Approval")
}

func (a *approver) warnUnrecognisedApproval(method string) {
	if a == nil || a.warn == nil || !looksLikeApprovalRequest(method) {
		return
	}
	a.warnMu.Lock()
	defer a.warnMu.Unlock()
	if a.warned[method] {
		return
	}
	if a.warned == nil {
		a.warned = map[string]bool{}
	}
	a.warned[method] = true
	fmt.Fprintf(a.warn, "herrscher: unrecognised approval request %q auto-accepted in session %q\n", method, a.session)
}

// approverFromEnv is the production constructor, kept apart from newApprover so
// tests never read the real environment.
func approverFromEnv() *approver { return newApprover(os.Getenv) }

// warnOneShotUngated tells the operator that this session's approval policy is
// not being enforced. `codex exec` has no approval channel, so a gated session
// falling onto that path runs ungated, and staying silent about it would leave
// an operator believing a mode they asked for is in force. It warns rather than
// refusing the turn: the operator decides whether to keep going.
func warnOneShotUngated(ap *approver, w io.Writer) {
	if ap == nil {
		return
	}
	fmt.Fprintf(w, "herrscher: codex exec has no approval channel, so session %q runs ungated on this path\n", ap.session)
}
