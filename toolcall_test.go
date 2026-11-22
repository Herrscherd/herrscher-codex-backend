package codex

import (
	"bufio"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	toolTestThread = "thread-1"
	toolTestTurn   = "turn-1"
	toolTestOutput = "stderr: token sk-secret"
)

func TestCommandStartCarriesItsItemIDAsTheStartedCall(t *testing.T) {
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem("call-1", "inProgress", "go test ./...", nil)),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{contracts.BackendKindTool, eventResult}) {
		t.Fatalf("kinds = %v, want a tool start then the result", got)
	}
	start := events[0]
	if start.Tool != toolCommand || start.Detail != "go test ./..." {
		t.Fatalf("start tool/detail = %q/%q, want %q/%q", start.Tool, start.Detail, toolCommand, "go test ./...")
	}
	call := mustToolCall(t, start)
	if call.ID != "call-1" || call.Tool != toolCommand || call.Detail != "go test ./..." || call.State != contracts.ToolCallStarted {
		t.Fatalf("call = %+v, want the started call call-1 of command with its detail", call)
	}
}

func TestSuccessfulCommandEndsItsCallAsSucceeded(t *testing.T) {
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem("call-1", "inProgress", "go test ./...", nil)),
		toolItemNotice(methodItemEnd, commandItem("call-1", "completed", "go test ./...", 0)),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{contracts.BackendKindTool, contracts.BackendKindToolDone, eventResult}) {
		t.Fatalf("kinds = %v, want one start, one finish and the result", got)
	}
	done := events[1]
	if done.Tool != "" || done.Detail != "" {
		t.Fatalf("finish carries tool %q and detail %q, want both empty", done.Tool, done.Detail)
	}
	call := mustToolCall(t, done)
	if call.ID != "call-1" || call.Tool != toolCommand || call.State != contracts.ToolCallSucceeded || call.Detail != "" || call.Error != "" {
		t.Fatalf("call = %+v, want call-1 of command succeeded with no detail and no error", call)
	}
}

func TestCommandExitingNonZeroEndsItsCallAsFailedWithTheExitCode(t *testing.T) {
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem("call-2", "inProgress", "go vet ./...", nil)),
		toolItemNotice(methodItemEnd, commandItem("call-2", "failed", "go vet ./...", 2)),
		toolTurnCompleted(),
	)
	call := mustToolCall(t, events[len(events)-2])
	if call.ID != "call-2" || call.State != contracts.ToolCallFailed {
		t.Fatalf("call = %+v, want call-2 failed", call)
	}
	if call.Error != "exit code 2" {
		t.Fatalf("error = %q, want %q: the error must not carry the command output", call.Error, "exit code 2")
	}
}

func TestCommandOutcomeEndsItsCallWithTheStateOfItsItem(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		exitCode  any
		wantState contracts.ToolCallState
		wantError string
	}{
		{name: "completed with exit code zero", status: "completed", exitCode: 0, wantState: contracts.ToolCallSucceeded},
		{name: "completed without exit code", status: "completed", exitCode: nil, wantState: contracts.ToolCallSucceeded},
		{name: "completed with a non zero exit code", status: "completed", exitCode: 1, wantState: contracts.ToolCallFailed, wantError: "exit code 1"},
		{name: "failed without exit code", status: "failed", exitCode: nil, wantState: contracts.ToolCallFailed, wantError: reasonCommandFailed},
		{name: "failed with a non zero exit code", status: "failed", exitCode: 127, wantState: contracts.ToolCallFailed, wantError: "exit code 127"},
		{name: "failed with an exit code of a million", status: "failed", exitCode: 1000000, wantState: contracts.ToolCallFailed, wantError: "exit code 1000000"},
		{name: "declined by the operator", status: "declined", exitCode: nil, wantState: contracts.ToolCallFailed, wantError: reasonCommandDeclined},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := runToolTurn(t, turnScope{threadID: toolTestThread},
				toolTurnStarted(),
				toolItemNotice(methodItemStart, commandItem("call-3", "inProgress", "make", nil)),
				toolItemNotice(methodItemEnd, commandItem("call-3", tt.status, "make", tt.exitCode)),
				toolTurnCompleted(),
			)
			call := mustToolCall(t, events[len(events)-2])
			if call.ID != "call-3" || call.State != tt.wantState || call.Error != tt.wantError {
				t.Fatalf("call = %+v, want call-3 state %v error %q", call, tt.wantState, tt.wantError)
			}
		})
	}
}

func TestCommandStillInProgressAtItsEndEmitsTheStartOnly(t *testing.T) {
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem("call-4", "inProgress", "sleep 1", nil)),
		toolItemNotice(methodItemEnd, commandItem("call-4", "inProgress", "sleep 1", nil)),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{contracts.BackendKindTool, eventResult}) {
		t.Fatalf("kinds = %v, want the start only before the result", got)
	}
}

func TestCommandWithoutItemIDEmitsAStartWithoutACall(t *testing.T) {
	item := commandItem("call-5", "inProgress", "ls", nil)
	delete(item, fieldID)
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, item),
		toolItemNotice(methodItemEnd, item),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{contracts.BackendKindTool, eventResult}) {
		t.Fatalf("kinds = %v, want the start alone before the result", got)
	}
	if events[0].ToolCall != nil || events[0].Tool != toolCommand || events[0].Detail != "ls" {
		t.Fatalf("event = %+v, want a legacy tool event without a call", events[0])
	}
}

func TestCommandWithoutTextEmitsNeitherStartNorFinish(t *testing.T) {
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem("call-6", "inProgress", "", nil)),
		toolItemNotice(methodItemEnd, commandItem("call-6", "completed", "", 0)),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{eventResult}) {
		t.Fatalf("kinds = %v, want the result alone", got)
	}
}

func TestCommandFinishWithoutTextEmitsNoFinish(t *testing.T) {
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem("call-6", "inProgress", "ls", nil)),
		toolItemNotice(methodItemEnd, commandItem("call-6", "completed", "", 0)),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{contracts.BackendKindTool, eventResult}) {
		t.Fatalf("kinds = %v, want the start alone before the result", got)
	}
}

func TestRepeatedStartOfAnOpenCallEmitsAToolEventWithoutCall(t *testing.T) {
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem("call-7", "inProgress", "go test ./...", nil)),
		toolItemNotice(methodItemStart, commandItem("call-7", "inProgress", "go test ./...", nil)),
		toolItemNotice(methodItemEnd, commandItem("call-7", "completed", "go test ./...", 0)),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{contracts.BackendKindTool, contracts.BackendKindTool, contracts.BackendKindToolDone, eventResult}) {
		t.Fatalf("kinds = %v, want one started call, a bare tool event, then one finish", got)
	}
	if events[1].ToolCall != nil || events[1].Tool != toolCommand || events[1].Detail != "go test ./..." {
		t.Fatalf("repeated start = %+v, want a bare tool event", events[1])
	}
	if call := mustToolCall(t, events[2]); call.ID != "call-7" || call.State != contracts.ToolCallSucceeded {
		t.Fatalf("finish = %+v, want call-7 succeeded: the repeated start must not touch the open call", call)
	}
}

func TestRepeatedFinishOfACallEmitsOnce(t *testing.T) {
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem("call-8", "inProgress", "make", nil)),
		toolItemNotice(methodItemEnd, commandItem("call-8", "completed", "make", 0)),
		toolItemNotice(methodItemEnd, commandItem("call-8", "completed", "make", 0)),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{contracts.BackendKindTool, contracts.BackendKindToolDone, eventResult}) {
		t.Fatalf("kinds = %v, want one finish for call-8", got)
	}
}

func TestFinishOfAnUnknownCallEmitsNothing(t *testing.T) {
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemEnd, commandItem("call-9", "completed", "make", 0)),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{eventResult}) {
		t.Fatalf("kinds = %v, want the result alone", got)
	}
}

func TestOpenCallsAreBoundedPerTurn(t *testing.T) {
	var messages []map[string]any
	messages = append(messages, toolTurnStarted())
	for index := 1; index <= maxOpenCalls+1; index++ {
		messages = append(messages, toolItemNotice(methodItemStart, commandItem(numberedCallID(index), "inProgress", "step", nil)))
	}
	for index := 1; index <= maxOpenCalls+1; index++ {
		messages = append(messages, toolItemNotice(methodItemEnd, commandItem(numberedCallID(index), "completed", "step", 0)))
	}
	messages = append(messages, toolTurnCompleted())
	events := runToolTurn(t, turnScope{threadID: toolTestThread}, messages...)

	var starts, finishes []string
	for index := range events {
		switch events[index].Kind {
		case contracts.BackendKindTool:
			if events[index].ToolCall != nil {
				starts = append(starts, events[index].ToolCall.ID)
			}
		case contracts.BackendKindToolDone:
			finishes = append(finishes, mustToolCall(t, events[index]).ID)
		}
	}
	if len(starts) != maxOpenCalls || starts[0] != numberedCallID(1) || starts[maxOpenCalls-1] != numberedCallID(maxOpenCalls) {
		t.Fatalf("started calls = %d, want the first %d only", len(starts), maxOpenCalls)
	}
	if len(finishes) != maxOpenCalls || finishes[maxOpenCalls-1] != numberedCallID(maxOpenCalls) {
		t.Fatalf("finished calls = %d, want the %d tracked calls only", len(finishes), maxOpenCalls)
	}
	for _, id := range finishes {
		if id == numberedCallID(maxOpenCalls+1) {
			t.Fatalf("call %s was finished although it never started as a call", id)
		}
	}
}

func TestOpenCallsDoNotSurviveTheTurn(t *testing.T) {
	first := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem("call-10", "inProgress", "sleep 1", nil)),
		toolTurnCompleted(),
	)
	if got := kindsOf(first); !equalStrings(got, []string{contracts.BackendKindTool, eventResult}) {
		t.Fatalf("first turn kinds = %v, want the start then the result", got)
	}
	second := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem("call-10", "inProgress", "sleep 1", nil)),
		toolItemNotice(methodItemEnd, commandItem("call-10", "completed", "sleep 1", 0)),
		toolTurnCompleted(),
	)
	if call := mustToolCall(t, second[0]); call.ID != "call-10" || call.State != contracts.ToolCallStarted {
		t.Fatalf("second turn start = %+v, want call-10 started again", call)
	}
	if got := kindsOf(second); !equalStrings(got, []string{contracts.BackendKindTool, contracts.BackendKindToolDone, eventResult}) {
		t.Fatalf("second turn kinds = %v, want the call closed in its own turn", got)
	}
}

func TestCompactionRepeatsFollowTheSameRule(t *testing.T) {
	compaction := map[string]any{fieldID: "c2", fieldType: itemContextCompaction}
	events := runToolTurn(t, turnScope{threadID: toolTestThread, compaction: true},
		toolItemNotice(methodItemStart, compaction),
		toolItemNotice(methodItemStart, compaction),
		toolItemNotice(methodItemEnd, compaction),
		toolItemNotice(methodItemEnd, compaction),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{contracts.BackendKindTool, contracts.BackendKindTool, contracts.BackendKindToolDone}) {
		t.Fatalf("kinds = %v, want one started compaction, a bare tool event, then one finish", got)
	}
	if events[1].ToolCall != nil || events[1].Tool != compactionTool {
		t.Fatalf("repeated compaction start = %+v, want a bare tool event", events[1])
	}
}

func TestCompactionStartAndItemCompletionCloseItsCall(t *testing.T) {
	compaction := map[string]any{fieldID: "c1", fieldType: itemContextCompaction}
	events := runToolTurn(t, turnScope{threadID: toolTestThread, compaction: true},
		toolItemNotice(methodItemStart, compaction),
		toolItemNotice(methodItemEnd, compaction),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{contracts.BackendKindTool, contracts.BackendKindToolDone}) {
		t.Fatalf("kinds = %v, want the compaction start then its finish", got)
	}
	done := mustToolCall(t, events[1])
	if done.ID != "c1" || done.Tool != compactionTool || done.State != contracts.ToolCallSucceeded {
		t.Fatalf("finish = %+v, want c1 of compaction succeeded", done)
	}
}

func TestEveryEmittedToolEventValidates(t *testing.T) {
	delta := notification(methodAgentText, map[string]any{fieldThreadID: toolTestThread, fieldTurnID: toolTestTurn, fieldDelta: "running"})
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		delta,
		toolItemNotice(methodItemStart, commandItem("call-1", "inProgress", "go test ./...", nil)),
		toolItemNotice(methodItemEnd, commandItem("call-1", "completed", "go test ./...", 0)),
		toolItemNotice(methodItemStart, commandItem("call-2", "inProgress", "go vet ./...", nil)),
		toolItemNotice(methodItemEnd, commandItem("call-2", "failed", "go vet ./...", 2)),
		toolTurnCompleted(),
	)
	if got := kindsOf(events); !equalStrings(got, []string{eventText, contracts.BackendKindTool, contracts.BackendKindToolDone, contracts.BackendKindTool, contracts.BackendKindToolDone, eventResult}) {
		t.Fatalf("kinds = %v, want text, two calls each started and finished, then the result", got)
	}
	for index := range events {
		if err := events[index].ValidateToolCall(); err != nil {
			t.Fatalf("event %q refused by ValidateToolCall: %v", events[index].Kind, err)
		}
	}
}

func runToolTurn(t *testing.T, scope turnScope, messages ...map[string]any) []contracts.BackendEvent {
	t.Helper()
	var input strings.Builder
	for _, message := range messages {
		line, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = input.Write(line)
		_, _ = input.WriteString("\n")
	}
	var events []contracts.BackendEvent
	onEvent := func(event contracts.BackendEvent) { events = append(events, event) }
	if _, err := readTurn(t.Context(), bufio.NewReader(strings.NewReader(input.String())), scope, onEvent, nil, nil); err != nil {
		t.Fatalf("readTurn: %v", err)
	}
	return events
}

func toolTurnStarted() map[string]any {
	return notification(methodTurnStart, map[string]any{fieldThreadID: toolTestThread, fieldTurn: map[string]any{fieldID: toolTestTurn}})
}

func toolTurnCompleted() map[string]any {
	return notification(methodTurnEnd, map[string]any{fieldThreadID: toolTestThread, fieldTurn: map[string]any{fieldID: toolTestTurn, fieldStatus: "completed"}})
}

func toolItemNotice(method string, item map[string]any) map[string]any {
	return notification(method, map[string]any{fieldThreadID: toolTestThread, fieldTurnID: toolTestTurn, fieldItem: item})
}

func commandItem(id, status, command string, exitCode any) map[string]any {
	return map[string]any{
		fieldID:            id,
		fieldType:          "commandExecution",
		fieldCommand:       command,
		"commandActions":   []any{},
		"cwd":              "/work/repo",
		fieldStatus:        status,
		fieldExitCode:      exitCode,
		"aggregatedOutput": toolTestOutput,
		"durationMs":       120,
	}
}

func mustToolCall(t *testing.T, event contracts.BackendEvent) contracts.ToolCall {
	t.Helper()
	if event.ToolCall == nil {
		t.Fatalf("event %q carries no tool call: %+v", event.Kind, event)
	}
	return *event.ToolCall
}

func kindsOf(events []contracts.BackendEvent) []string {
	kinds := make([]string, 0, len(events))
	for index := range events {
		kinds = append(kinds, events[index].Kind)
	}
	return kinds
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func numberedCallID(index int) string {
	return "call-bound-" + strconv.Itoa(index)
}
