package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var _ contracts.Compactor = (*streamResponder)(nil)

const (
	methodCompactStart    = "thread/compact/start"
	itemContextCompaction = "contextCompaction"
	compactionTool        = "compaction"
	compactNote           = "Context compacted by Codex, size after compaction not measured."
	envCompactTimeout     = "CODEX_COMPACT_TIMEOUT"
	defaultCompactTimeout = 10 * time.Minute
	maxReasonRunes        = 200
)

type compactUnavailableError struct {
	reason string
	cause  error
}

var _ contracts.NoticeError = (*compactUnavailableError)(nil)

func (e *compactUnavailableError) Error() string {
	return "codex compaction unavailable: " + e.reason
}

func (e *compactUnavailableError) Unwrap() error {
	return e.cause
}

func (e *compactUnavailableError) Notice() contracts.Notice {
	return contracts.Notice{Code: contracts.CodeCompactUnavailable, Args: []contracts.Arg{contracts.Text("Reason", noticeReason(e.reason))}}
}

func unavailable(what string, err error) error {
	if _, typed := errors.AsType[*compactUnavailableError](err); typed {
		return err
	}
	return &compactUnavailableError{reason: what + ": " + err.Error(), cause: err}
}

func noticeReason(cause string) string {
	line := firstNonBlankLine(cause)
	if utf8.RuneCountInString(line) <= maxReasonRunes {
		return line
	}
	return string([]rune(line)[:maxReasonRunes-1]) + "…"
}

func firstNonBlankLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var (
	errCompactTimeoutNotPositive = &compactUnavailableError{reason: "a compaction timeout must be a positive duration"}
	errCompactInstruction        = &compactUnavailableError{reason: "thread/compact/start takes no instruction, and the backend has no field for one"}
	errCompactNoThread           = &compactUnavailableError{reason: "no turn has run yet, so there is no thread to compact"}
	errCompactionRejected        = errors.New("the compaction was rejected")
	errCompactionFailed          = errors.New("the compaction turn failed")
)

func (r *streamResponder) Compact(ctx context.Context, req contracts.CompactRequest, onEvent func(contracts.BackendEvent)) (contracts.CompactResult, error) {
	if strings.TrimSpace(req.Instruction) != "" {
		return contracts.CompactResult{}, errCompactInstruction
	}
	ctx, cancel := r.compactDeadline(ctx)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess == nil {
		if r.resumeID == "" {
			return contracts.CompactResult{}, errCompactNoThread
		}
		if err := r.open(ctx, r.resumeID); err != nil {
			return contracts.CompactResult{}, unavailable("reopening the thread", err)
		}
	}
	if err := r.sess.Compact(ctx, onEvent); err != nil {
		if !errors.Is(err, errCompactionRejected) && !errors.Is(err, errCompactionFailed) {
			r.dropSession()
		}
		return contracts.CompactResult{}, unavailable("the compaction failed", err)
	}
	return contracts.CompactResult{Note: compactNote, Notice: contracts.Notice{Code: contracts.CodeCompacted}}, nil
}

func parseCompactTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", envCompactTimeout, raw, err)
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("%s=%q: %w", envCompactTimeout, raw, errCompactTimeoutNotPositive)
	}
	return timeout, nil
}

func (r *streamResponder) compactDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.compactTimeout
	if timeout == 0 {
		timeout = defaultCompactTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func (r *streamResponder) dropSession() {
	r.resumeID = r.sess.threadID
	if err := r.sess.Close(); err != nil {
		slog.Warn("app-server session did not close cleanly after a lost compaction", "error", err)
	}
	r.sess = nil
}

func compactStartRequest(requestID int, thread string) map[string]any {
	return map[string]any{fieldID: requestID, fieldMethod: methodCompactStart, fieldParams: map[string]any{fieldThreadID: thread}}
}

func (s *appSession) Compact(ctx context.Context, onEvent func(contracts.BackendEvent)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	requestID := s.nextID
	s.nextID++
	if err := s.write(compactStartRequest(requestID, s.threadID)); err != nil {
		return err
	}
	turn, err := s.awaitTurn(ctx, turnScope{threadID: s.threadID, compaction: true}, onEvent)
	if err != nil {
		return err
	}
	if turn.rejected {
		return &compactUnavailableError{reason: fmt.Sprintf("%s: %s", errCompactionRejected, turn.ErrMsg), cause: errCompactionRejected}
	}
	if turn.IsError {
		return &compactUnavailableError{reason: fmt.Sprintf("%s: %s", errCompactionFailed, turn.ErrMsg), cause: errCompactionFailed}
	}
	return nil
}

func isContextCompactionItem(item map[string]any) bool {
	return stringField(item, fieldType) == itemContextCompaction
}
