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

type turnScope struct {
	threadID   string
	requestID  int
	compaction bool
}

const (
	maxParkedCompletions = 16
	maxParkedRequests    = 16
)

type turnCollector struct {
	scope          turnScope
	onEvent        func(contracts.BackendEvent)
	warner         *approver
	turnID         string
	parked         []map[string]any
	parkedRequests []map[string]any
	result         turnResult
	deltas         strings.Builder
	observed       bool
}

func readTurn(ctx context.Context, reader *bufio.Reader, scope turnScope, onEvent func(contracts.BackendEvent), approvals *approver, respondHoldingSendLock func(map[string]any) error) (turnResult, error) {
	approvals = approvalsForTurn(ctx, approvals)
	collector := &turnCollector{scope: scope, onEvent: onEvent, warner: warnerFor(approvals)}
	result, err := collector.read(ctx, reader, approvals, respondHoldingSendLock)
	if declined := collector.declineParkedRequests(respondHoldingSendLock); declined != nil {
		err = errors.Join(err, declined)
	}
	return result, err
}

func (c *turnCollector) read(ctx context.Context, reader *bufio.Reader, approvals *approver, respondHoldingSendLock func(map[string]any) error) (turnResult, error) {
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			result, done, handleErr := c.consumeLine(ctx, line, approvals, respondHoldingSendLock)
			if done || handleErr != nil {
				return result, handleErr
			}
		}
		if err != nil {
			return c.readFailure(err)
		}
	}
}

func (c *turnCollector) consumeLine(ctx context.Context, line []byte, approvals *approver, respondHoldingSendLock func(map[string]any) error) (turnResult, bool, error) {
	var msg map[string]any
	if err := json.Unmarshal(line, &msg); err != nil {
		slog.Debug("skipping a line from codex that is not a JSON object", "error", err)
		return turnResult{}, false, nil
	}
	c.observed = true
	if rpcErr, ok := msg[fieldError].(map[string]any); ok {
		return c.reject(rpcErr), true, nil
	}
	if err := c.adoptTurnResponse(msg); err != nil {
		return turnResult{}, true, err
	}
	if err := c.serveParkedRequests(ctx, approvals, respondHoldingSendLock); err != nil {
		return turnResult{}, true, err
	}
	if c.result.completed {
		return c.outcome(), true, nil
	}
	if isServerRequest(msg) {
		return turnResult{}, false, c.answerRequest(ctx, msg, approvals, respondHoldingSendLock)
	}
	if err := c.handleNotification(msg); err != nil {
		return turnResult{}, true, err
	}
	if err := c.serveParkedRequests(ctx, approvals, respondHoldingSendLock); err != nil {
		return turnResult{}, true, err
	}
	if c.result.completed {
		return c.outcome(), true, nil
	}
	return turnResult{}, false, nil
}

func (c *turnCollector) answerRequest(ctx context.Context, msg map[string]any, approvals *approver, respondHoldingSendLock func(map[string]any) error) error {
	params := mapField(msg, fieldParams)
	method := stringField(msg, fieldMethod)
	if c.scope.threadID != "" {
		if threadID := stringField(params, fieldThreadID); threadID != c.scope.threadID {
			slog.Error("approval declined: the request names another thread", "method", method, "expected", c.scope.threadID, "got", threadID)
			return respondHoldingSendLock(approvalResponse(msg[fieldID], false))
		}
	}
	turn := turnIDOf(params)
	if turn == "" && looksLikeApprovalRequest(method) {
		c.warner.warnDeclined(causeNoTurnID, method)
		return respondHoldingSendLock(approvalResponse(msg[fieldID], false))
	}
	if turn != "" && c.turnID == "" {
		return c.parkRequest(msg, method, respondHoldingSendLock)
	}
	if turn != "" && turn != c.turnID {
		slog.Error("approval declined: the request names another turn", "method", method, "expected", c.turnID, "got", turn)
		return respondHoldingSendLock(approvalResponse(msg[fieldID], false))
	}
	return answerApproval(ctx, msg, msg[fieldID], approvals, respondHoldingSendLock)
}

func (c *turnCollector) parkRequest(msg map[string]any, method string, respondHoldingSendLock func(map[string]any) error) error {
	if len(c.parkedRequests) == maxParkedRequests {
		slog.Error("approval declined: too many requests arrived before the turn id was known", "method", method, "limit", maxParkedRequests)
		return respondHoldingSendLock(approvalResponse(msg[fieldID], false))
	}
	c.parkedRequests = append(c.parkedRequests, msg)
	return nil
}

func (c *turnCollector) serveParkedRequests(ctx context.Context, approvals *approver, respondHoldingSendLock func(map[string]any) error) error {
	if c.turnID == "" {
		return nil
	}
	for len(c.parkedRequests) > 0 {
		msg := c.parkedRequests[0]
		c.parkedRequests = c.parkedRequests[1:]
		if err := c.answerRequest(ctx, msg, approvals, respondHoldingSendLock); err != nil {
			return err
		}
	}
	return nil
}

func (c *turnCollector) declineParkedRequests(respondHoldingSendLock func(map[string]any) error) error {
	var errs []error
	for _, msg := range c.parkedRequests {
		slog.Error("approval declined: the turn ended with the request unanswered", "method", stringField(msg, fieldMethod))
		errs = append(errs, respondHoldingSendLock(approvalResponse(msg[fieldID], false)))
	}
	c.parkedRequests = nil
	return errors.Join(errs...)
}

func isServerRequest(msg map[string]any) bool {
	_, hasID := msg[fieldID]
	return hasID && msg[fieldMethod] != nil
}

func (c *turnCollector) adoptTurnResponse(msg map[string]any) error {
	if c.scope.requestID == 0 || msg[fieldMethod] != nil {
		return nil
	}
	if id, ok := msg[fieldID].(float64); !ok || int(id) != c.scope.requestID {
		return nil
	}
	return c.learnTurnID(stringField(mapField(mapField(msg, fieldResult), fieldTurn), fieldID))
}

func (c *turnCollector) handleNotification(msg map[string]any) error {
	params := mapField(msg, fieldParams)
	if c.scope.threadID != "" && stringField(params, fieldThreadID) != c.scope.threadID {
		return nil
	}
	switch stringField(msg, fieldMethod) {
	case methodTurnStart:
		return c.learnTurnID(stringField(mapField(params, fieldTurn), fieldID))
	case methodTurnEnd:
		return c.observeCompletion(mapField(params, fieldTurn))
	case methodItemStart:
		if c.scope.compaction && isContextCompactionItem(mapField(params, fieldItem)) {
			if err := c.learnTurnID(turnIDOf(params)); err != nil {
				return err
			}
		}
	}
	if c.belongs(params) {
		c.handleAppEvent(msg)
	}
	return nil
}

func (c *turnCollector) learnTurnID(turnID string) error {
	if turnID == "" {
		return nil
	}
	if c.turnID == "" {
		c.turnID = turnID
		c.releaseParked()
		return nil
	}
	if c.turnID != turnID {
		return fmt.Errorf("%w: codex named turn %q and then turn %q", errTurnIDMismatch, c.turnID, turnID)
	}
	return nil
}

func (c *turnCollector) observeCompletion(turn map[string]any) error {
	if c.turnID == "" {
		return c.park(turn)
	}
	if stringField(turn, fieldID) == c.turnID {
		c.complete(turn)
	}
	return nil
}

func (c *turnCollector) park(turn map[string]any) error {
	if len(c.parked) == maxParkedCompletions {
		return fmt.Errorf("%w (queue of %d): turn %q ended before its id was known", errParkedFull, maxParkedCompletions, stringField(turn, fieldID))
	}
	c.parked = append(c.parked, turn)
	return nil
}

func (c *turnCollector) releaseParked() {
	parked := c.parked
	c.parked = nil
	for _, turn := range parked {
		if stringField(turn, fieldID) == c.turnID {
			c.complete(turn)
			return
		}
	}
}

func (c *turnCollector) belongs(params map[string]any) bool {
	return c.turnID == "" || turnIDOf(params) == c.turnID
}

func turnIDOf(params map[string]any) string {
	if id := stringField(params, fieldTurnID); id != "" {
		return id
	}
	return stringField(mapField(params, fieldTurn), fieldID)
}

func (c *turnCollector) reject(rpcErr map[string]any) turnResult {
	c.result.IsError = true
	c.result.rejected = true
	c.result.ErrMsg = stringField(rpcErr, fieldMessage)
	if c.result.ErrMsg == "" {
		c.result.ErrMsg = "Codex rejected the turn"
	}
	c.emitResult()
	return c.result
}

func (c *turnCollector) outcome() turnResult {
	if c.result.Text == "" {
		c.result.Text = c.deltas.String()
	}
	return c.result
}

func (c *turnCollector) readFailure(err error) (turnResult, error) {
	if !c.observed {
		return c.result, errors.Join(errTurnNotStarted, err)
	}
	return c.result, fmt.Errorf("codex read turn: %w", err)
}

func (c *turnCollector) handleAppEvent(msg map[string]any) {
	params := mapField(msg, fieldParams)
	switch stringField(msg, fieldMethod) {
	case methodAgentText:
		if !c.scope.compaction {
			c.appendDelta(stringField(params, fieldDelta))
		}
	case methodItemStart:
		c.observeStarted(mapField(params, fieldItem))
	case methodItemEnd:
		c.observeCompleted(mapField(params, fieldItem))
	case methodError:
		c.observeError(params)
	}
}

func (c *turnCollector) appendDelta(delta string) {
	if delta == "" {
		return
	}
	_, _ = c.deltas.WriteString(delta)
	c.emit(contracts.BackendEvent{Kind: eventText, Detail: delta})
}

func (c *turnCollector) observeStarted(item map[string]any) {
	if c.scope.compaction {
		if isContextCompactionItem(item) {
			c.emitToolStart(item, compactionTool, "")
		}
		return
	}
	if isCommandItem(item) {
		c.startCommand(item)
		return
	}
	c.observeItem(item)
}

func (c *turnCollector) observeCompleted(item map[string]any) {
	if c.scope.compaction {
		if isContextCompactionItem(item) {
			c.emitToolDone(item, compactionTool, contracts.ToolCallSucceeded, "")
		}
		return
	}
	if isCommandItem(item) {
		c.finishCommand(item)
		return
	}
	c.observeItem(item)
}

func (c *turnCollector) observeItem(item map[string]any) {
	itemType := stringField(item, fieldType)
	if itemType != "agentMessage" && itemType != "agent_message" {
		return
	}
	if v, ok := item[fieldText].(string); ok && v != "" {
		c.result.Text = v
	}
}

func (c *turnCollector) observeError(params map[string]any) {
	if retry, ok := params[fieldWillRetry].(bool); ok && retry {
		return
	}
	c.result.IsError = true
	if message := stringField(mapField(params, fieldError), fieldMessage); message != "" {
		c.result.ErrMsg = message
	}
}

func (c *turnCollector) complete(turn map[string]any) {
	if stringField(turn, fieldStatus) == statusFailed {
		c.result.IsError = true
	}
	if c.result.ErrMsg == "" {
		c.result.ErrMsg = stringField(mapField(turn, fieldError), fieldMessage)
	}
	c.result.completed = true
	c.emitResult()
}

func (c *turnCollector) emitResult() {
	if !c.scope.compaction {
		c.emit(contracts.BackendEvent{Kind: eventResult, IsError: c.result.IsError})
	}
}

func (c *turnCollector) emit(event contracts.BackendEvent) {
	if c.onEvent != nil {
		c.onEvent(event)
	}
}
