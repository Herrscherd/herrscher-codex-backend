package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type approver struct {
	session, bin string
	warn         io.Writer
	warnMu       sync.Mutex
	warned       map[string]bool
	ask          func(ctx context.Context, bin string, argv []string) ([]byte, error)
}

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

type approvalRequest struct {
	tool, subject string
}

func approvalSubject(method string, params map[string]any) (approvalRequest, bool) {
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		return approvalRequest{tool: "Bash", subject: stringField(params, "command")}, true
	case "item/fileChange/requestApproval", "applyPatchApproval":
		changes := mapField(params, "fileChanges")
		paths := make([]string, 0, len(changes))
		for p := range changes {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		return approvalRequest{tool: "Write", subject: strings.Join(paths, " ")}, true
	}
	return approvalRequest{}, false
}

func approvalResponse(id any, allow bool) map[string]any {
	decision := "decline"
	if allow {
		decision = "accept"
	}
	return map[string]any{"id": id, fieldResult: map[string]any{"decision": decision}}
}

func approvalPolicy(gated bool) string {
	if gated {
		return "on-request"
	}
	return "never"
}

func answerApproval(ctx context.Context, msg map[string]any, id any, ap *approver, respond func(map[string]any) error) error {
	method := stringField(msg, fieldMethod)
	params := mapField(msg, "params")
	request, known := approvalSubject(method, params)
	if !known {
		ap.warnUnrecognisedApproval(method)
		return respond(approvalResponse(id, true))
	}
	return respond(approvalResponse(id, ap.allow(ctx, request.tool, request.subject)))
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
		a.warned = make(map[string]bool)
	}
	a.warned[method] = true
	writeWarning(a.warn, fmt.Sprintf("herrscher: unrecognised approval request %q auto-accepted in session %q\n", method, a.session))
}

func approverFromEnv() *approver { return newApprover(os.Getenv) }

func warnOneShotUngated(ap *approver, w io.Writer) {
	if ap == nil {
		return
	}
	writeWarning(w, fmt.Sprintf("herrscher: codex exec has no approval channel, so session %q runs ungated on this path\n", ap.session))
}

func writeWarning(w io.Writer, message string) {
	if _, err := io.WriteString(w, message); err != nil {
		slog.Warn("approval warning not written", "error", err)
	}
}
