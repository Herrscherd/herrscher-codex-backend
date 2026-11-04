package codex

import (
	"context"
	"encoding/json"
	"errors"
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
	gate := contracts.ApprovalsFromEnv(look)
	if !gate.Gated {
		return nil
	}
	return &approver{session: gate.Session, bin: gate.Bin, ask: runApprove, warn: os.Stderr}
}

func runApprove(ctx context.Context, bin string, argv []string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, argv...).Output()
}

var (
	errVerdictUnreadable = errors.New("the approver answer is not a verdict")
	errVerdictMissing    = errors.New("the approver answer names no decision")
)

func (a *approver) allow(ctx context.Context, tool, subject string) bool {
	if a == nil {
		return true
	}
	decision, err := a.decide(ctx, tool, subject)
	if err != nil {
		slog.Error("approval declined: the approver did not answer", "session", a.session, "tool", tool, "error", err)
		return false
	}
	return decision == "allow"
}

func (a *approver) decide(ctx context.Context, tool, subject string) (string, error) {
	out, err := a.ask(ctx, a.bin, []string{
		"approve", "ask",
		"--session", a.session,
		"--tool", tool,
		"--subject", subject,
	})
	if err != nil {
		return "", err
	}
	var verdict struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(out, &verdict); err != nil {
		return "", fmt.Errorf("%w: %w", errVerdictUnreadable, err)
	}
	if verdict.Decision == "" {
		return "", errVerdictMissing
	}
	return verdict.Decision, nil
}

type approvalRequest struct {
	tool, subject string
}

func approvalSubject(method string, params map[string]any) (approvalRequest, bool) {
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		return approvalRequest{tool: toolBash, subject: stringField(params, fieldCommand)}, true
	case "item/fileChange/requestApproval", "applyPatchApproval":
		changes := mapField(params, fieldFileChanges)
		paths := make([]string, 0, len(changes))
		for p := range changes {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		return approvalRequest{tool: toolWrite, subject: strings.Join(paths, " ")}, true
	}
	return approvalRequest{}, false
}

func approvalResponse(requestID any, allow bool) map[string]any {
	decision := "decline"
	if allow {
		decision = "accept"
	}
	return map[string]any{"id": requestID, fieldResult: map[string]any{"decision": decision}}
}

func approvalPolicy(gated bool) string {
	if gated {
		return "on-request"
	}
	return "never"
}

func answerApproval(ctx context.Context, msg map[string]any, requestID any, approvals *approver, respondHoldingSendLock func(map[string]any) error) error {
	method := stringField(msg, fieldMethod)
	params := mapField(msg, fieldParams)
	request, known := approvalSubject(method, params)
	if !known {
		return respondHoldingSendLock(approvalResponse(requestID, approvals.unrecognisedAllowed(method)))
	}
	return respondHoldingSendLock(approvalResponse(requestID, approvals.allow(ctx, request.tool, request.subject)))
}

func (a *approver) unrecognisedAllowed(method string) bool {
	if a == nil {
		return true
	}
	a.warnUnrecognisedApproval(method)
	return false
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
	writeWarning(a.warn, fmt.Sprintf("herrscher: unrecognised approval request %q declined in session %q\n", method, a.session))
}

func approverFromEnv() *approver { return newApprover(os.Getenv) }

func warnOneShotUngated(approvals *approver, diagnostics io.Writer) {
	if approvals == nil {
		return
	}
	writeWarning(diagnostics, fmt.Sprintf("herrscher: codex exec has no approval channel, so session %q runs ungated on this path\n", approvals.session))
}

func writeWarning(w io.Writer, message string) {
	if _, err := io.WriteString(w, message); err != nil {
		slog.Warn("approval warning not written", "error", err)
	}
}
