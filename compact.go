package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var _ contracts.Compactor = (*streamResponder)(nil)

const (
	methodCompactStart    = "thread/compact/start"
	itemContextCompaction = "contextCompaction"
	compactionTool        = "compaction"
	compactNote           = "Contexte compacté par Codex, taille après compaction non mesurée."
	compactTimeout        = 10 * time.Minute
)

var (
	errCompactInstruction = errors.New("codex compaction takes no instruction: thread/compact/start has no field for one")
	errCompactNoThread    = errors.New("codex compaction needs an open thread: no turn has run yet")
	errCompactionRejected = errors.New("codex rejected the compaction")
	errCompactionFailed   = errors.New("codex compaction turn failed")
)

func (r *streamResponder) Compact(ctx context.Context, req contracts.CompactRequest, onEvent func(contracts.BackendEvent)) (contracts.CompactResult, error) {
	if strings.TrimSpace(req.Instruction) != "" {
		return contracts.CompactResult{}, errCompactInstruction
	}
	ctx, cancel := compactDeadline(ctx)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess == nil {
		if r.resumeID == "" {
			return contracts.CompactResult{}, errCompactNoThread
		}
		if err := r.open(ctx, r.resumeID); err != nil {
			return contracts.CompactResult{}, fmt.Errorf("codex compaction: %w", err)
		}
	}
	if err := r.sess.Compact(ctx, onEvent); err != nil {
		if !errors.Is(err, errCompactionRejected) && !errors.Is(err, errCompactionFailed) {
			r.dropSession()
		}
		return contracts.CompactResult{}, fmt.Errorf("codex compaction: %w", err)
	}
	return contracts.CompactResult{Note: compactNote}, nil
}

func compactDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, compactTimeout)
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
		return fmt.Errorf("%w: %s", errCompactionRejected, turn.ErrMsg)
	}
	if turn.IsError {
		return fmt.Errorf("%w: %s", errCompactionFailed, turn.ErrMsg)
	}
	return nil
}

func isContextCompactionItem(item map[string]any) bool {
	return stringField(item, fieldType) == itemContextCompaction
}
