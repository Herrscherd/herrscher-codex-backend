package codex

import (
	"log/slog"
	"math"
	"strconv"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	reasonCommandFailed   = "command failed"
	reasonCommandDeclined = "command declined"
	maxOpenCalls          = 256
	maxSeenCalls          = 4096
	maxCallIDBytes        = 256
)

type callTracker struct {
	open      map[string]struct{}
	seen      map[string]struct{}
	openBound boundReport
	seenBound boundReport
}

type boundReport struct {
	name     string
	limit    int
	reported bool
}

func (t *callTracker) begin(callID string) bool {
	if len(callID) > maxCallIDBytes {
		return false
	}
	if _, known := t.seen[callID]; known {
		return false
	}
	if len(t.seen) >= maxSeenCalls {
		t.seenBound.warnOnce()
		return false
	}
	if t.seen == nil {
		t.open = make(map[string]struct{})
		t.seen = make(map[string]struct{})
		t.openBound = boundReport{name: "max_open", limit: maxOpenCalls}
		t.seenBound = boundReport{name: "max_seen", limit: maxSeenCalls}
	}
	t.seen[callID] = struct{}{}
	if len(t.open) >= maxOpenCalls {
		t.openBound.warnOnce()
		return false
	}
	t.open[callID] = struct{}{}
	return true
}

func (b *boundReport) warnOnce() {
	if b.reported {
		return
	}
	b.reported = true
	slog.Warn("tool calls reach a bound of the turn, later starts carry no lifecycle", "bound", b.name, "limit", b.limit)
}

func (t *callTracker) end(callID string) bool {
	if _, open := t.open[callID]; !open {
		return false
	}
	delete(t.open, callID)
	return true
}

func isCommandItem(item map[string]any) bool {
	itemType := stringField(item, fieldType)
	return itemType == "commandExecution" || itemType == "command_execution"
}

func (c *turnCollector) startCommand(item map[string]any) {
	command := stringField(item, fieldCommand)
	if command == "" {
		return
	}
	c.emitToolStart(item, toolCommand, command)
}

func (c *turnCollector) finishCommand(item map[string]any) {
	state, reason, finished := commandOutcome(item)
	if !finished {
		return
	}
	c.emitToolDone(item, toolCommand, state, reason)
}

func commandOutcome(item map[string]any) (contracts.ToolCallState, string, bool) {
	exitReason, exited := nonZeroExit(item)
	switch stringField(item, fieldStatus) {
	case statusCompleted:
		if exited {
			return contracts.ToolCallFailed, exitReason, true
		}
		return contracts.ToolCallSucceeded, "", true
	case statusFailed:
		if exited {
			return contracts.ToolCallFailed, exitReason, true
		}
		return contracts.ToolCallFailed, reasonCommandFailed, true
	case statusDeclined:
		return contracts.ToolCallFailed, reasonCommandDeclined, true
	}
	return 0, "", false
}

func nonZeroExit(item map[string]any) (string, bool) {
	code, ok := exitCodeField(item)
	if !ok || code == 0 {
		return "", false
	}
	return "exit code " + strconv.Itoa(code), true
}

func exitCodeField(item map[string]any) (int, bool) {
	value, ok := numberField(item, fieldExitCode)
	if !ok || value != math.Trunc(value) || value < math.MinInt32 || value > math.MaxInt32 {
		return 0, false
	}
	return int(value), true
}

func (c *turnCollector) emitToolStart(item map[string]any, tool, detail string) {
	event := contracts.BackendEvent{Kind: contracts.BackendKindTool, Tool: tool, Detail: detail}
	if id := stringField(item, fieldID); id != "" && c.calls.begin(id) {
		event.ToolCall = &contracts.ToolCall{ID: id, Tool: tool, Detail: detail, State: contracts.ToolCallStarted}
	}
	c.emit(event)
}

func (c *turnCollector) emitToolDone(item map[string]any, tool string, state contracts.ToolCallState, reason string) {
	callID := stringField(item, fieldID)
	if callID == "" || !c.calls.end(callID) {
		return
	}
	c.emit(contracts.BackendEvent{
		Kind:     contracts.BackendKindToolDone,
		ToolCall: &contracts.ToolCall{ID: callID, Tool: tool, State: state, Error: reason},
	})
}
