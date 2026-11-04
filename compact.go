package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var _ contracts.Compactor = (*streamResponder)(nil)

const (
	methodCompactStart    = "thread/compact/start"
	itemContextCompaction = "contextCompaction"
	compactionTool        = "compaction"
	compactNote           = "Contexte compacté par Codex, taille après compaction non mesurée."
)

var (
	errCompactInstruction = errors.New("codex compaction takes no instruction: thread/compact/start has no field for one")
	errCompactionRejected = errors.New("codex rejected the compaction")
	errCompactionFailed   = errors.New("codex compaction turn failed")
)

func (r *streamResponder) Compact(ctx context.Context, req contracts.CompactRequest, onEvent func(contracts.BackendEvent)) (contracts.CompactResult, error) {
	if strings.TrimSpace(req.Instruction) != "" {
		return contracts.CompactResult{}, errCompactInstruction
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess == nil {
		if err := r.open(ctx, r.resumeID); err != nil {
			return contracts.CompactResult{}, fmt.Errorf("codex compaction: %w", err)
		}
	}
	if err := r.sess.Compact(ctx, onEvent); err != nil {
		return contracts.CompactResult{}, fmt.Errorf("codex compaction: %w", err)
	}
	return contracts.CompactResult{Note: compactNote}, nil
}

func compactStartRequest(requestID int, thread string) map[string]any {
	return map[string]any{"id": requestID, fieldMethod: methodCompactStart, fieldParams: map[string]any{fieldThreadID: thread}}
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
	outcome := make(chan error, 1)
	go func() {
		outcome <- readCompaction(ctx, s.out, onEvent, s.approvalsIfGated, s.write)
	}()
	select {
	case err := <-outcome:
		return err
	case <-ctx.Done():
		s.abort()
		if readErr := <-outcome; readErr != nil {
			slog.Debug("compaction ended with the aborted session", "error", readErr)
		}
		return ctx.Err()
	}
}

func readCompaction(ctx context.Context, reader *bufio.Reader, onEvent func(contracts.BackendEvent), approvals *approver, respondHoldingSendLock func(map[string]any) error) error {
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			finished, handleErr := consumeCompactionLine(ctx, line, onEvent, approvals, respondHoldingSendLock)
			if finished || handleErr != nil {
				return handleErr
			}
		}
		if err != nil {
			return fmt.Errorf("codex read compaction: %w", err)
		}
	}
}

func consumeCompactionLine(ctx context.Context, line []byte, onEvent func(contracts.BackendEvent), approvals *approver, respondHoldingSendLock func(map[string]any) error) (bool, error) {
	var msg map[string]any
	if err := json.Unmarshal(line, &msg); err != nil {
		slog.Debug("skipping a line from codex that is not a JSON object", "error", err)
		return false, nil
	}
	if rpcErr, ok := msg[fieldError].(map[string]any); ok {
		return true, rejectedCompaction(rpcErr)
	}
	if isServerRequest(msg) {
		return false, answerApproval(ctx, msg, msg["id"], approvals, respondHoldingSendLock)
	}
	return handleCompactionNotification(msg, onEvent)
}

func rejectedCompaction(rpcErr map[string]any) error {
	message := stringField(rpcErr, "message")
	if message == "" {
		message = "Codex rejected the compaction"
	}
	return fmt.Errorf("%w: %s", errCompactionRejected, message)
}

func handleCompactionNotification(msg map[string]any, onEvent func(contracts.BackendEvent)) (bool, error) {
	params := mapField(msg, fieldParams)
	switch stringField(msg, fieldMethod) {
	case "item/started":
		if isContextCompactionItem(mapField(params, "item")) && onEvent != nil {
			onEvent(contracts.BackendEvent{Kind: "tool", Tool: compactionTool})
		}
	case "turn/completed":
		turn := mapField(params, "turn")
		if status := stringField(turn, fieldStatus); status == "failed" || status == statusError {
			return true, fmt.Errorf("%w: %s", errCompactionFailed, stringField(turn, fieldError))
		}
		return true, nil
	case "turn/failed":
		return true, fmt.Errorf("%w: %s", errCompactionFailed, stringField(params, "message"))
	case methodError:
		if retry, ok := params["willRetry"].(bool); ok && retry {
			return false, nil
		}
		return true, fmt.Errorf("%w: %s", errCompactionFailed, stringField(mapField(params, fieldError), "message"))
	}
	return false, nil
}

func isContextCompactionItem(item map[string]any) bool {
	return stringField(item, "type") == itemContextCompaction
}
