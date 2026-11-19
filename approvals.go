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
	"strconv"
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
	errVerdictUnreadable   = errors.New("the approver answer is not a verdict")
	errVerdictMissing      = errors.New("the approver answer names no decision")
	errNoApprovalChannel   = errors.New("the session has no approver and the turn carries no daemon asker")
	errAnswerUnreadable    = errors.New("the daemon answer names no decision")
	errAnswerMismatch      = errors.New("the daemon answered another approval request")
	errUngatedSession      = errors.New("codex session was started without approvals, so a turn that asks for approval cannot be gated")
	errApprovalIDUnusable  = errors.New("the approval request carries no usable id")
	errApprovalChannelLost = errors.New("codex session was put on approval by a turn's asker, and this turn carries no asker or approver")
)

func (a *approver) hasChannel() bool {
	return a != nil && a.ask != nil
}

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
	if a.ask == nil {
		return "", errNoApprovalChannel
	}
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

func (a *approver) verdict(ctx context.Context, requestID string, request approvalRequest) bool {
	asker, asked := contracts.ApprovalAskerFrom(ctx)
	if !asked {
		return a.allow(ctx, request.tool, request.subject)
	}
	return askDaemon(ctx, asker, requestID, request)
}

func askDaemon(ctx context.Context, asker contracts.ApprovalAsker, requestID string, request approvalRequest) bool {
	answer, err := requestFromDaemon(ctx, asker, requestID, request)
	if err != nil {
		slog.Error("approval declined: the daemon did not answer", "tool", request.tool, "error", err)
		return false
	}
	return answer.Allowed()
}

func requestFromDaemon(ctx context.Context, asker contracts.ApprovalAsker, requestID string, request approvalRequest) (contracts.ApprovalAnswer, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ApprovalAnswer{}, err
	}
	answer := asker(ctx, contracts.ApprovalRequest{ID: requestID, Tool: request.tool, Subject: request.subject})
	if err := ctx.Err(); err != nil {
		return contracts.ApprovalAnswer{}, err
	}
	if answer.ID != requestID {
		return contracts.ApprovalAnswer{}, errAnswerMismatch
	}
	if _, err := contracts.ParseApprovalDecision(string(answer.Decision)); err != nil {
		return contracts.ApprovalAnswer{}, fmt.Errorf("%w: %w", errAnswerUnreadable, err)
	}
	return answer, nil
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

func approvalID(method string, requestID any, params map[string]any) (string, error) {
	key, ok := requestKey(requestID)
	if !ok {
		return "", errApprovalIDUnusable
	}
	return strings.Join(nonEmpty(method, key, stringField(params, fieldThreadID), turnIDOf(params), stringField(params, fieldItemID)), "/"), nil
}

func requestKey(requestID any) (string, bool) {
	switch number := requestID.(type) {
	case float64:
		return strconv.FormatFloat(number, 'f', -1, 64), true
	case string:
		return number, number != ""
	}
	return "", false
}

func nonEmpty(values ...string) []string {
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			kept = append(kept, value)
		}
	}
	return kept
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

func approvalsForTurn(turnCtx context.Context, approvals *approver) *approver {
	if approvals != nil {
		return approvals
	}
	if _, asked := contracts.ApprovalAskerFrom(turnCtx); asked {
		return &approver{warn: os.Stderr}
	}
	return nil
}

func answerApproval(ctx context.Context, msg map[string]any, requestID any, approvals *approver, respondHoldingSendLock func(map[string]any) error) error {
	method := stringField(msg, fieldMethod)
	params := mapField(msg, fieldParams)
	request, known := approvalSubject(method, params)
	if !known {
		return respondHoldingSendLock(approvalResponse(requestID, approvals.unrecognisedAllowed(ctx, method)))
	}
	approvalKey, err := approvalID(method, requestID, params)
	if err != nil {
		slog.Error("approval declined: the request carries no usable id", "method", method, "error", err)
		return respondHoldingSendLock(approvalResponse(requestID, false))
	}
	return respondHoldingSendLock(approvalResponse(requestID, approvals.verdict(ctx, approvalKey, request)))
}

func (a *approver) unrecognisedAllowed(ctx context.Context, method string) bool {
	if a == nil {
		a = approvalsForTurn(ctx, nil)
	}
	if a == nil {
		return true
	}
	a.warnDeclinedApproval(method)
	return false
}

func looksLikeApprovalRequest(method string) bool {
	return strings.HasSuffix(method, "requestApproval") || strings.HasSuffix(method, "Approval")
}

func (a *approver) warnDeclinedApproval(method string) {
	if a == nil || !looksLikeApprovalRequest(method) {
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
	writer := a.warn
	if writer == nil {
		writer = os.Stderr
	}
	writeWarning(writer, fmt.Sprintf("herrscher: approval request %q declined in session %q\n", method, a.session))
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
