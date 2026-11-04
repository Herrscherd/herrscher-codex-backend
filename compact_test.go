package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type compactPeer struct {
	session  *appSession
	requests chan map[string]any
	written  chan string
	hangUp   func()
}

func (p compactPeer) requireWritten(t *testing.T, marker string) {
	t.Helper()
	timeout := time.After(time.Second)
	for {
		select {
		case line := <-p.written:
			if strings.Contains(line, marker) {
				return
			}
		case <-timeout:
			t.Fatalf("no %s was read by the client before the turn returned", marker)
		}
	}
}

type signalingWriter struct {
	out     io.Writer
	written chan<- string
}

func (w signalingWriter) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	select {
	case w.written <- string(p):
	default:
	}
	return n, err
}

func newCompactPeer(t *testing.T, reply func(request map[string]any) []map[string]any) compactPeer {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	written := make(chan string, 64)
	peer := compactPeer{
		session:  newAppSession(inW, outR),
		requests: make(chan map[string]any, 8),
		written:  written,
		hangUp:   func() { closeChecked(t, outW) },
	}
	peer.session.threadID = "thread-1"
	go serveCompactRequests(inR, signalingWriter{out: outW, written: written}, peer.requests, reply)
	t.Cleanup(func() {
		closeChecked(t, inR)
		closeChecked(t, inW)
		closeChecked(t, outR)
	})
	return peer
}

func serveCompactRequests(in io.Reader, out io.Writer, requests chan<- map[string]any, reply func(map[string]any) []map[string]any) {
	reader := bufio.NewReader(in)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var request map[string]any
		if json.Unmarshal(line, &request) != nil {
			continue
		}
		requests <- request
		for _, message := range reply(request) {
			if !writeCompactMessage(out, message) {
				return
			}
		}
	}
}

func writeCompactMessage(out io.Writer, message map[string]any) bool {
	payload, err := json.Marshal(message)
	if err != nil {
		return false
	}
	_, err = out.Write(append(payload, '\n'))
	return err == nil
}

func turnStartReply(request map[string]any) map[string]any {
	return map[string]any{fieldID: request[fieldID], fieldResult: map[string]any{fieldTurn: map[string]any{fieldID: "turn-1", fieldStatus: "inProgress", "items": []any{}}}}
}

func answerCompaction(notifications ...map[string]any) func(map[string]any) []map[string]any {
	return func(request map[string]any) []map[string]any {
		messages := []map[string]any{{"id": request["id"], "result": make(map[string]any)}, turnStartedNotice("turn-1")}
		return append(messages, notifications...)
	}
}

func notification(method string, params map[string]any) map[string]any {
	return map[string]any{"method": method, "params": params}
}

func completedTurn() map[string]any {
	return turnEnded("completed", nil)
}

func turnEnded(status string, turnError map[string]any) map[string]any {
	return turnEndedAs("turn-1", status, turnError)
}

func turnEndedAs(turnID, status string, turnError map[string]any) map[string]any {
	turn := map[string]any{fieldID: turnID, fieldStatus: status}
	if turnError != nil {
		turn[fieldError] = turnError
	}
	return notification(methodTurnEnd, map[string]any{fieldThreadID: "thread-1", fieldTurn: turn})
}

func turnStartedNotice(turnID string) map[string]any {
	return notification(methodTurnStart, map[string]any{fieldThreadID: "thread-1", fieldTurn: map[string]any{fieldID: turnID}})
}

func errorNotice(willRetry bool, message string) map[string]any {
	return notification("error", map[string]any{
		fieldThreadID:  "thread-1",
		fieldTurnID:    "turn-1",
		fieldWillRetry: willRetry,
		fieldError:     map[string]any{fieldMessage: message},
	})
}

func compactingResponder(peer compactPeer) *streamResponder {
	return &streamResponder{
		sess: peer.session,
		start: func(context.Context, string) (*appSession, error) {
			return nil, errors.New("unexpected session open")
		},
	}
}

func TestCompactStartsTheCompactionOfTheThread(t *testing.T) {
	peer := newCompactPeer(t, answerCompaction(completedTurn()))
	if _, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	request := <-peer.requests
	if request["method"] != "thread/compact/start" {
		t.Fatalf("method=%v, want thread/compact/start", request["method"])
	}
	params, ok := request["params"].(map[string]any)
	if !ok || params["threadId"] != "thread-1" || len(params) != 1 {
		t.Fatalf("params=%v, want only the threadId", params)
	}
}

func TestCompactSendsABlankInstructionAsNone(t *testing.T) {
	peer := newCompactPeer(t, answerCompaction(completedTurn()))
	if _, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{Instruction: "   "}, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if request := <-peer.requests; request["method"] != "thread/compact/start" {
		t.Fatalf("method=%v, want thread/compact/start", request["method"])
	}
}

func TestCompactRefusesAFreeInstructionBeforeOpeningASession(t *testing.T) {
	opened := 0
	r := &streamResponder{resumeID: "thread-1", start: func(context.Context, string) (*appSession, error) {
		opened++
		return nil, errors.New("must not open")
	}}
	_, err := r.Compact(t.Context(), contracts.CompactRequest{Instruction: "garde les décisions"}, nil)
	if !errors.Is(err, errCompactInstruction) {
		t.Fatalf("err=%v, want errCompactInstruction", err)
	}
	if opened != 0 {
		t.Fatalf("opened %d sessions, want none", opened)
	}
}

func TestCompactRefusesWhenNoThreadIsOpenOrResumable(t *testing.T) {
	opened := 0
	r := &streamResponder{start: func(context.Context, string) (*appSession, error) {
		opened++
		return nil, errors.New("must not open")
	}}
	_, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
	if !errors.Is(err, errCompactNoThread) {
		t.Fatalf("err=%v, want errCompactNoThread", err)
	}
	if opened != 0 {
		t.Fatalf("opened %d sessions, want none", opened)
	}
}

func TestCompactOpensTheThreadWhenNoSessionIsOpen(t *testing.T) {
	peer := newCompactPeer(t, answerCompaction(completedTurn()))
	var resumed []string
	r := &streamResponder{resumeID: "thread-1", start: func(_ context.Context, resume string) (*appSession, error) {
		resumed = append(resumed, resume)
		return peer.session, nil
	}}
	if _, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(resumed) != 1 || resumed[0] != "thread-1" {
		t.Fatalf("opened with %v, want one open on thread-1", resumed)
	}
	if request := <-peer.requests; request["method"] != "thread/compact/start" {
		t.Fatalf("method=%v, want thread/compact/start", request["method"])
	}
}

func TestCompactReportsNoMeasuredSizeEvenWhenUsageArrives(t *testing.T) {
	usage := notification("thread/tokenUsage/updated", map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"tokenUsage": map[string]any{
			"last":  map[string]any{"inputTokens": 30, "outputTokens": 5, "cachedInputTokens": 0, "reasoningOutputTokens": 0, "totalTokens": 35},
			"total": map[string]any{"inputTokens": 900, "outputTokens": 60, "cachedInputTokens": 0, "reasoningOutputTokens": 0, "totalTokens": 960},
		},
	})
	peer := newCompactPeer(t, answerCompaction(usage, completedTurn()))
	got, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got.TokensMeasured || got.TokensAfter != 0 || got.Done {
		t.Fatalf("result=%+v, want no measured size and not done", got)
	}
	if got.Note != compactNote {
		t.Fatalf("note=%q, want %q", got.Note, compactNote)
	}
}

func TestCompactReportsItsStartAsAToolEventOnly(t *testing.T) {
	started := notification("item/started", map[string]any{fieldThreadID: "thread-1", fieldTurnID: "turn-1", fieldItem: map[string]any{"id": "c1", fieldType: itemContextCompaction}})
	summary := notification("item/agentMessage/delta", map[string]any{fieldThreadID: "thread-1", fieldTurnID: "turn-1", fieldDelta: "résumé de la conversation"})
	peer := newCompactPeer(t, answerCompaction(started, summary, completedTurn()))
	var events []contracts.BackendEvent
	if _, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, func(e contracts.BackendEvent) {
		events = append(events, e)
	}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "tool" || events[0].Tool != compactionTool {
		t.Fatalf("events=%+v, want one tool event named compaction", events)
	}
}

func TestCompactEmitsNoToolEventForAnotherItem(t *testing.T) {
	message := notification("item/started", map[string]any{fieldThreadID: "thread-1", fieldTurnID: "turn-1", fieldItem: map[string]any{"id": "m1", fieldType: "agentMessage"}})
	peer := newCompactPeer(t, answerCompaction(message, completedTurn()))
	var events []contracts.BackendEvent
	if _, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, func(e contracts.BackendEvent) {
		events = append(events, e)
	}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events=%+v, want none for an agent message", events)
	}
}

func TestCompactRunsWithoutAnEventSink(t *testing.T) {
	started := notification("item/started", map[string]any{fieldThreadID: "thread-1", fieldTurnID: "turn-1", fieldItem: map[string]any{"id": "c1", fieldType: itemContextCompaction}})
	peer := newCompactPeer(t, answerCompaction(started, completedTurn()))
	if _, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil); err != nil {
		t.Fatalf("Compact without a sink: %v", err)
	}
}

func TestCompactWrapsARejectedRequest(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		return []map[string]any{{"id": request["id"], "error": map[string]any{"message": "compaction refused"}}}
	})
	_, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil)
	if !errors.Is(err, errCompactionRejected) || !strings.Contains(err.Error(), "compaction refused") {
		t.Fatalf("err=%v, want a wrapped errCompactionRejected carrying the message", err)
	}
}

func TestCompactWrapsAFailedCompaction(t *testing.T) {
	cases := []struct {
		name          string
		notifications []map[string]any
	}{
		{name: "turn completed as failed", notifications: []map[string]any{turnEnded("failed", map[string]any{fieldMessage: "context overflow"})}},
		{name: "error that will not retry, then the turn ends", notifications: []map[string]any{errorNotice(false, "context overflow"), turnEnded("failed", nil)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newCompactPeer(t, answerCompaction(tc.notifications...))
			_, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil)
			if !errors.Is(err, errCompactionFailed) || !strings.Contains(err.Error(), "context overflow") {
				t.Fatalf("err=%v, want a wrapped errCompactionFailed carrying the message", err)
			}
		})
	}
}

func TestCompactIgnoresAnErrorThatWillRetry(t *testing.T) {
	peer := newCompactPeer(t, answerCompaction(errorNotice(true, "transient"), completedTurn()))
	if _, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

func TestCompactIgnoresEventsOfOtherTurnsAndThreads(t *testing.T) {
	peer := newCompactPeer(t, answerCompaction(
		notification("item/started", map[string]any{fieldThreadID: "thread-2", fieldTurnID: "turn-1", fieldItem: map[string]any{fieldType: itemContextCompaction}}),
		notification("turn/completed", map[string]any{fieldThreadID: "thread-1", fieldTurn: map[string]any{fieldID: "turn-0", fieldStatus: "failed"}}),
		notification("turn/completed", map[string]any{fieldThreadID: "thread-2", fieldTurn: map[string]any{fieldID: "turn-1", fieldStatus: "failed"}}),
		notification("item/started", map[string]any{fieldThreadID: "thread-1", fieldTurnID: "turn-1", fieldItem: map[string]any{fieldType: itemContextCompaction}}),
		notification("turn/completed", map[string]any{fieldThreadID: "thread-1", fieldTurn: map[string]any{fieldID: "turn-1", fieldStatus: "completed"}}),
	))
	var tools int
	if _, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, func(e contracts.BackendEvent) {
		if e.Kind == "tool" {
			tools++
		}
	}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if tools != 1 {
		t.Fatalf("tool events=%d, want only the compaction of this thread and turn", tools)
	}
}

func TestCompactAnswersAServerRequestMidCompaction(t *testing.T) {
	approval := map[string]any{fieldID: float64(900), fieldMethod: "item/unknown/requestApproval", fieldParams: map[string]any{fieldThreadID: "thread-1"}}
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] == methodCompactStart {
			return []map[string]any{turnStartedNotice("turn-1"), approval}
		}
		if request[fieldID] == float64(900) {
			return []map[string]any{completedTurn()}
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := compactingResponder(peer).Compact(ctx, contracts.CompactRequest{}, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	for len(peer.requests) > 0 {
		if request := <-peer.requests; request[fieldID] == float64(900) && request[fieldResult] != nil {
			return
		}
	}
	t.Fatal("the server request was never answered during the compaction")
}

func TestCompactHonorsCancellation(t *testing.T) {
	peer := newCompactPeer(t, func(map[string]any) []map[string]any { return nil })
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-peer.requests
		cancel()
	}()
	_, err := compactingResponder(peer).Compact(ctx, contracts.CompactRequest{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

func TestCompactDropsTheSessionItLostAndReopensTheThread(t *testing.T) {
	lost := newCompactPeer(t, func(map[string]any) []map[string]any { return nil })
	fresh := newCompactPeer(t, answerCompaction(completedTurn()))
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-lost.requests
		cancel()
	}()
	r := compactingResponder(lost)
	r.resumeID = ""
	if _, err := r.Compact(ctx, contracts.CompactRequest{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if r.sess != nil {
		t.Fatal("the session stayed attached after the compaction was cancelled")
	}
	if r.resumeID != "thread-1" {
		t.Fatalf("resumeID=%q, want the lost session's thread-1", r.resumeID)
	}
	var resumed []string
	r.start = func(_ context.Context, resume string) (*appSession, error) {
		resumed = append(resumed, resume)
		return fresh.session, nil
	}
	if _, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil); err != nil {
		t.Fatalf("Compact after the loss: %v", err)
	}
	if len(resumed) != 1 || resumed[0] != "thread-1" {
		t.Fatalf("reopened with %v, want thread-1", resumed)
	}
}

func TestCompactKeepsTheSessionAfterARejection(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		return []map[string]any{{"id": request["id"], "error": map[string]any{"message": "compaction refused"}}}
	})
	r := compactingResponder(peer)
	if _, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil); err == nil {
		t.Fatal("Compact succeeded on a refusal")
	}
	if r.sess != peer.session {
		t.Fatal("a refused compaction dropped a session that is still alive")
	}
}

func TestCompactFailsWhenTheServerHangsUp(t *testing.T) {
	peer := newCompactPeer(t, func(map[string]any) []map[string]any { return nil })
	go func() {
		<-peer.requests
		peer.hangUp()
	}()
	_, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err=%v, want the end of the stream (io.EOF)", err)
	}
}

func TestCompactTimeoutIsTenMinutes(t *testing.T) {
	ctx, cancel := compactDeadline(t.Context())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the compaction has no deadline")
	}
	if remaining := time.Until(deadline); remaining > 10*time.Minute || remaining < 10*time.Minute-time.Minute {
		t.Fatalf("deadline in %v, want 10m", remaining)
	}
}

func TestManifestDeclaresTheCompactor(t *testing.T) {
	if !backendPlugin(t).Manifest.Capabilities.Compactor {
		t.Fatal("the codex manifest does not declare Capabilities.Compactor")
	}
}

func compactionStarted(turnID string) map[string]any {
	return notification(methodItemStart, map[string]any{fieldThreadID: "thread-1", fieldTurnID: turnID, fieldItem: map[string]any{"id": "c1", fieldType: itemContextCompaction}})
}

func TestCompactIgnoresTheCompletionOfAnotherTurnOfItsThread(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != methodCompactStart {
			return nil
		}
		return []map[string]any{{fieldID: request[fieldID], fieldResult: make(map[string]any)}, turnEndedAs("turn-0", "completed", nil)}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_, err := compactingResponder(peer).Compact(ctx, contracts.CompactRequest{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want context.DeadlineExceeded: the compaction closed on the completion of another turn", err)
	}
}

func TestCompactEndsOnTheCompletionOfItsOwnTurn(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != methodCompactStart {
			return nil
		}
		return []map[string]any{
			{fieldID: request[fieldID], fieldResult: make(map[string]any)},
			turnEndedAs("turn-0", "completed", nil),
			compactionStarted("turn-1"),
			turnEndedAs("turn-1", "completed", nil),
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := compactingResponder(peer).Compact(ctx, contracts.CompactRequest{}, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

func TestCompactFailsWhenSeventeenForeignCompletionsEndBeforeItsItem(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != methodCompactStart {
			return nil
		}
		messages := []map[string]any{{fieldID: request[fieldID], fieldResult: make(map[string]any)}}
		messages = append(messages, earlyCompletions(17)...)
		return append(messages, compactionStarted("turn-1"))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r := compactingResponder(peer)
	_, err := r.Compact(ctx, contracts.CompactRequest{}, nil)
	if !errors.Is(err, errParkedFull) || !strings.Contains(err.Error(), "turn-18") {
		t.Fatalf("err=%v, want errParkedFull naming turn-18 without waiting for the context", err)
	}
	if r.sess != nil {
		t.Fatal("the session that dropped an unnamed completion was kept")
	}
}

func TestCompactFailsWhenItsItemNamesAnotherTurnThanTurnStarted(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != methodCompactStart {
			return nil
		}
		return []map[string]any{
			{fieldID: request[fieldID], fieldResult: make(map[string]any)},
			turnStartedNotice("turn-1"),
			compactionStarted("turn-2"),
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := compactingResponder(peer).Compact(ctx, contracts.CompactRequest{}, nil)
	if !errors.Is(err, errTurnIDMismatch) || !strings.Contains(err.Error(), "turn-1") || !strings.Contains(err.Error(), "turn-2") {
		t.Fatalf("err=%v, want errTurnIDMismatch naming turn-1 and turn-2", err)
	}
}

func TestCompactRefusesWhenTheOpenFails(t *testing.T) {
	r := &streamResponder{resumeID: "thread-1", start: func(context.Context, string) (*appSession, error) {
		return nil, errors.New("open refused")
	}}
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("Compact panicked instead of refusing: %v", p)
			}
		}()
		_, err = r.Compact(t.Context(), contracts.CompactRequest{}, nil)
	}()
	if err == nil || !strings.Contains(err.Error(), "open refused") {
		t.Fatalf("err=%v, want the open failure", err)
	}
}
