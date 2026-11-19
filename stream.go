package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type streamResponder struct {
	start                     func(turnCtx context.Context, resume string) (*appSession, error)
	base                      []string
	model, effort, dir        string
	verbose                   bool
	resumeID                  string
	env                       map[string]string
	gatewayHomeRemovedOnClose string
	approvalsIfGated          *approver
	compactTimeout            time.Duration
	mu                        sync.Mutex
	sess                      *appSession
	tokenMu                   sync.Mutex
	token                     string
}

var errTurnNotStarted = errors.New("codex: app-server closed before any turn output")

var errTurnIDMismatch = errors.New("codex named two ids for one turn")

var errParkedFull = errors.New("codex ended more turns than it could hold before naming the turn of its response")

var (
	errTurnFailed = errors.New("codex turn failed")
	errNoThreadID = errors.New("codex app-server returned no thread id")
)

func (r *streamResponder) rememberToken(threadID string) {
	r.tokenMu.Lock()
	defer r.tokenMu.Unlock()
	r.token = threadID
}

func (r *streamResponder) abandonUnsettled(err error) error {
	if errors.Is(err, errTurnIDMismatch) || errors.Is(err, errParkedFull) {
		r.dropSession()
	}
	return err
}

func (r *streamResponder) Respond(ctx context.Context, prompt contracts.Prompt, onEvent func(contracts.BackendEvent)) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess == nil {
		if err := r.open(ctx, r.resumeID); err != nil {
			return "", err
		}
	}
	if err := r.prepareTurn(ctx); err != nil {
		return "", err
	}
	content := withContext(prompt.Context, withAttachments(prompt.Content, prompt.Attachments))
	result, err := r.sess.Send(ctx, content, onEvent)
	if err != nil {
		if !restartable(err) {
			return "", turnError(r.abandonUnsettled(err))
		}
		if result, err = r.reopen(ctx, content, onEvent); err != nil {
			return "", r.abandonUnsettled(err)
		}
	}
	if result.IsError {
		return result.Text, fmt.Errorf("%w: %s", errTurnFailed, result.ErrMsg)
	}
	return result.Text, nil
}

func (r *streamResponder) prepareTurn(ctx context.Context) error {
	if err := r.relaunchForAsker(ctx); err != nil {
		return err
	}
	return r.admit(ctx)
}

func (r *streamResponder) relaunchForAsker(ctx context.Context) error {
	_, asked := contracts.ApprovalAskerFrom(ctx)
	if asked && r.sess.approvalsIfGated == nil {
		if r.sess.threadID == "" {
			return errUngatedSession
		}
		r.dropSession()
		if err := r.open(ctx, r.resumeID); err != nil {
			return err
		}
	}
	return nil
}

func (r *streamResponder) admit(ctx context.Context) error {
	_, asked := contracts.ApprovalAskerFrom(ctx)
	gate := r.sess.approvalsIfGated
	if asked && gate == nil {
		return errUngatedSession
	}
	if !asked && gate != nil && !gate.hasChannel() {
		return errApprovalChannelLost
	}
	return nil
}

func (r *streamResponder) open(ctx context.Context, resume string) error {
	session, err := r.start(ctx, resume)
	if err != nil {
		return err
	}
	r.sess = session
	if session.approvalsIfGated != nil {
		r.approvalsIfGated = session.approvalsIfGated
	}
	r.rememberToken(session.threadID)
	return nil
}

func (r *streamResponder) reopen(ctx context.Context, content string, onEvent func(contracts.BackendEvent)) (turnResult, error) {
	if onEvent != nil {
		onEvent(contracts.BackendEvent{Kind: "reset"})
	}
	resume := r.sess.threadID
	if closeErr := r.sess.Close(); closeErr != nil {
		slog.Warn("previous app-server session did not close cleanly", "error", closeErr)
	}
	if err := r.open(ctx, resume); err != nil {
		return turnResult{}, err
	}
	return r.sess.Send(ctx, content, onEvent)
}

func restartable(err error) bool {
	return errors.Is(err, errTurnNotStarted) && !isContextError(err)
}

func turnError(err error) error {
	if isContextError(err) {
		return err
	}
	return fmt.Errorf("codex turn: %w", err)
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (r *streamResponder) ResumeToken() string {
	r.tokenMu.Lock()
	defer r.tokenMu.Unlock()
	if r.token == "" {
		return r.resumeID
	}
	return r.token
}

func (r *streamResponder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var sessErr error
	if r.sess != nil {
		sessErr = r.sess.Close()
	}
	var homeErr error
	if r.gatewayHomeRemovedOnClose != "" {
		homeErr = os.RemoveAll(r.gatewayHomeRemovedOnClose)
	}
	return errors.Join(sessErr, homeErr)
}

func streamBase(fields []string) []string {
	if len(fields) == 0 {
		return []string{codexName}
	}
	return fields
}

func (r *streamResponder) launchedWithin(lifetime context.Context) func(context.Context, string) (*appSession, error) {
	return func(turnCtx context.Context, resume string) (*appSession, error) {
		return startAppSession(lifetime, turnCtx, r.base, r.model, r.effort, r.dir, r.verbose, resume, r.env, r.approvalsIfGated)
	}
}

func appServerArgv(base []string, gated bool) []string {
	argv := append([]string{}, base...)
	argv = append(argv,
		"-c", `approval_policy="`+approvalPolicy(gated)+`"`,
		"-c", `sandbox_mode="workspace-write"`,
		"-c", `mcp_servers.neublox.default_tools_approval_mode="approve"`,
	)
	return append(argv, "app-server", "--listen", "stdio://")
}
