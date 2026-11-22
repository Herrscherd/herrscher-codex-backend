package codex

import (
	"strconv"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	reasonCommandFailed   = "command failed"
	reasonCommandDeclined = "command declined"
	maxOpenCalls          = 256
)

type callTracker struct {
	ids map[string]struct{}
}

func (t *callTracker) begin(callID string) bool {
	if _, open := t.ids[callID]; open || len(t.ids) >= maxOpenCalls {
		return false
	}
	if t.ids == nil {
		t.ids = make(map[string]struct{})
	}
	t.ids[callID] = struct{}{}
	return true
}

func (t *callTracker) end(callID string) bool {
	if _, open := t.ids[callID]; !open {
		return false
	}
	delete(t.ids, callID)
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
	code, ok := numberField(item, fieldExitCode)
	if !ok || code == 0 {
		return "", false
	}
	return "exit code " + strconv.FormatInt(int64(code), 10), true
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
