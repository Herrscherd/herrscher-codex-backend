package codex

import (
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

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

	starts := startedIDs(events)
	finishes := finishedIDs(t, events)
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

func TestClosedCallFreesItsOpenSlot(t *testing.T) {
	messages := []map[string]any{toolTurnStarted()}
	for index := 1; index <= maxOpenCalls; index++ {
		messages = append(messages, toolItemNotice(methodItemStart, commandItem(numberedCallID(index), "inProgress", "step", nil)))
	}
	messages = append(messages,
		toolItemNotice(methodItemEnd, commandItem(numberedCallID(1), "completed", "step", 0)),
		toolItemNotice(methodItemStart, commandItem(numberedCallID(maxOpenCalls+1), "inProgress", "step", nil)),
		toolItemNotice(methodItemEnd, commandItem(numberedCallID(maxOpenCalls+1), "completed", "step", 0)),
		toolTurnCompleted(),
	)
	events := runToolTurn(t, turnScope{threadID: toolTestThread}, messages...)

	starts := startedIDs(events)
	finishes := finishedIDs(t, events)
	if len(starts) != maxOpenCalls+1 || starts[maxOpenCalls] != numberedCallID(maxOpenCalls+1) {
		t.Fatalf("started calls = %d, want the start that follows a closed call to start", len(starts))
	}
	if len(finishes) != 2 || finishes[0] != numberedCallID(1) || finishes[1] != numberedCallID(maxOpenCalls+1) {
		t.Fatalf("finished calls = %v, want calls 1 and %d", finishes, maxOpenCalls+1)
	}
}

func TestStartRefusedByTheOpenBoundStaysBareAfterASlotFrees(t *testing.T) {
	refused := numberedCallID(maxOpenCalls + 1)
	messages := []map[string]any{toolTurnStarted()}
	for index := 1; index <= maxOpenCalls; index++ {
		messages = append(messages, toolItemNotice(methodItemStart, commandItem(numberedCallID(index), "inProgress", "step", nil)))
	}
	messages = append(messages,
		toolItemNotice(methodItemStart, commandItem(refused, "inProgress", "step", nil)),
		toolItemNotice(methodItemEnd, commandItem(numberedCallID(1), "completed", "step", 0)),
		toolItemNotice(methodItemStart, commandItem(refused, "inProgress", "step", nil)),
		toolItemNotice(methodItemEnd, commandItem(refused, "completed", "step", 0)),
		toolTurnCompleted(),
	)
	events := runToolTurn(t, turnScope{threadID: toolTestThread}, messages...)

	for _, id := range startedIDs(events) {
		if id == refused {
			t.Fatalf("call %s started although its first start was refused by the open bound", id)
		}
	}
	if finishes := finishedIDs(t, events); len(finishes) != 1 || finishes[0] != numberedCallID(1) {
		t.Fatalf("finished calls = %v, want call 1 only", finishes)
	}
	if bare := bareToolStarts(events); len(bare) != 2 || bare[0].Detail != "step" || bare[1].Detail != "step" {
		t.Fatalf("bare starts = %+v, want both starts of the refused call as bare tool events", bare)
	}
}

func bareToolStarts(events []contracts.BackendEvent) []contracts.BackendEvent {
	var bare []contracts.BackendEvent
	for index := range events {
		if events[index].Kind == contracts.BackendKindTool && events[index].ToolCall == nil {
			bare = append(bare, events[index])
		}
	}
	return bare
}

func TestSeenCallsAreBoundedPerTurn(t *testing.T) {
	messages := []map[string]any{toolTurnStarted()}
	for index := 1; index <= maxSeenCalls+1; index++ {
		id := numberedCallID(index)
		messages = append(messages,
			toolItemNotice(methodItemStart, commandItem(id, "inProgress", "step", nil)),
			toolItemNotice(methodItemEnd, commandItem(id, "completed", "step", 0)),
		)
	}
	messages = append(messages, toolTurnCompleted())
	events := runToolTurn(t, turnScope{threadID: toolTestThread}, messages...)

	starts := startedIDs(events)
	finishes := finishedIDs(t, events)
	if len(starts) != maxSeenCalls || starts[maxSeenCalls-1] != numberedCallID(maxSeenCalls) {
		t.Fatalf("started calls = %d, want the first %d only", len(starts), maxSeenCalls)
	}
	if len(finishes) != maxSeenCalls || finishes[maxSeenCalls-1] != numberedCallID(maxSeenCalls) {
		t.Fatalf("finished calls = %d, want the %d started calls only", len(finishes), maxSeenCalls)
	}
}

func TestEachBoundWarnsOncePerTurn(t *testing.T) {
	logs := captureLog(t)
	messages := []map[string]any{toolTurnStarted()}
	for index := 1; index <= maxOpenCalls+2; index++ {
		messages = append(messages, toolItemNotice(methodItemStart, commandItem(numberedCallID(index), "inProgress", "step", nil)))
	}
	for index := 1; index <= maxOpenCalls+2; index++ {
		messages = append(messages, toolItemNotice(methodItemEnd, commandItem(numberedCallID(index), "completed", "step", 0)))
	}
	for index := maxOpenCalls + 3; index <= maxSeenCalls+2; index++ {
		id := numberedCallID(index)
		messages = append(messages,
			toolItemNotice(methodItemStart, commandItem(id, "inProgress", "step", nil)),
			toolItemNotice(methodItemEnd, commandItem(id, "completed", "step", 0)),
		)
	}
	messages = append(messages, toolTurnCompleted())
	runToolTurn(t, turnScope{threadID: toolTestThread}, messages...)

	for _, bound := range []string{"bound=max_open", "bound=max_seen"} {
		if count := strings.Count(logs.String(), bound); count != 1 {
			t.Fatalf("warnings naming %s after one turn = %d, want 1 in\n%s", bound, count, logs.String())
		}
	}

	runToolTurn(t, turnScope{threadID: toolTestThread}, messages...)

	for _, bound := range []string{"bound=max_open", "bound=max_seen"} {
		if count := strings.Count(logs.String(), bound); count != 2 {
			t.Fatalf("warnings naming %s after two turns = %d, want 2 in\n%s", bound, count, logs.String())
		}
	}
}

func TestCallIDBeyondTheLengthBoundNeverOpensACall(t *testing.T) {
	longID := strings.Repeat("x", maxCallIDBytes+1)
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem(longID, "inProgress", "make", nil)),
		toolItemNotice(methodItemEnd, commandItem(longID, "completed", "make", 0)),
		toolTurnCompleted(),
	)

	if starts, finishes := startedIDs(events), finishedIDs(t, events); len(starts) != 0 || len(finishes) != 0 {
		t.Fatalf("started = %d, finished = %d, want no call for an id over %d bytes", len(starts), len(finishes), maxCallIDBytes)
	}
	if bare := bareToolStarts(events); len(bare) != 1 || bare[0].Detail != "make" {
		t.Fatalf("bare starts = %+v, want the start as a bare tool event", bare)
	}
}

func TestCallIDAtTheLengthBoundOpensACall(t *testing.T) {
	boundID := strings.Repeat("x", maxCallIDBytes)
	events := runToolTurn(t, turnScope{threadID: toolTestThread},
		toolTurnStarted(),
		toolItemNotice(methodItemStart, commandItem(boundID, "inProgress", "make", nil)),
		toolItemNotice(methodItemEnd, commandItem(boundID, "completed", "make", 0)),
		toolTurnCompleted(),
	)

	if finishes := finishedIDs(t, events); len(finishes) != 1 || finishes[0] != boundID {
		t.Fatalf("finished calls = %d, want the call whose id is exactly %d bytes", len(finishes), maxCallIDBytes)
	}
}
