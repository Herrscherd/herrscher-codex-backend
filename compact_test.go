package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type compactPeer struct {
	session  *appSession
	requests chan map[string]any
	hangUp   func()
}

func newCompactPeer(t *testing.T, reply func(request map[string]any) []map[string]any) compactPeer {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	peer := compactPeer{
		session:  newAppSession(inW, outR),
		requests: make(chan map[string]any, 8),
		hangUp:   func() { closeChecked(t, outW) },
	}
	peer.session.threadID = "thread-1"
	go serveCompactRequests(inR, outW, peer.requests, reply)
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

func answerCompaction(notifications ...map[string]any) func(map[string]any) []map[string]any {
	return func(request map[string]any) []map[string]any {
		messages := []map[string]any{{"id": request["id"], "result": make(map[string]any)}}
		return append(messages, notifications...)
	}
}

func notification(method string, params map[string]any) map[string]any {
	return map[string]any{"method": method, "params": params}
}

func completedTurn() map[string]any {
	return notification("turn/completed", map[string]any{"turn": map[string]any{"status": "completed"}})
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
	r := &streamResponder{start: func(context.Context, string) (*appSession, error) {
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
	started := notification("item/started", map[string]any{"item": map[string]any{"id": "c1", "type": "contextCompaction"}})
	summary := notification("item/agentMessage/delta", map[string]any{"delta": "résumé de la conversation"})
	peer := newCompactPeer(t, answerCompaction(started, summary, completedTurn()))
	var events []contracts.BackendEvent
	if _, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, func(e contracts.BackendEvent) {
		events = append(events, e)
	}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "tool" || events[0].Tool != "compaction" {
		t.Fatalf("events=%+v, want one tool event named compaction", events)
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
		name         string
		notification map[string]any
	}{
		{name: "turn completed as failed", notification: notification("turn/completed", map[string]any{"turn": map[string]any{"status": "failed", "error": "context overflow"}})},
		{name: "turn failed", notification: notification("turn/failed", map[string]any{"message": "context overflow"})},
		{name: "error that will not retry", notification: notification("error", map[string]any{"willRetry": false, "error": map[string]any{"message": "context overflow"}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newCompactPeer(t, answerCompaction(tc.notification))
			_, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil)
			if !errors.Is(err, errCompactionFailed) || !strings.Contains(err.Error(), "context overflow") {
				t.Fatalf("err=%v, want a wrapped errCompactionFailed carrying the message", err)
			}
		})
	}
}

func TestCompactIgnoresAnErrorThatWillRetry(t *testing.T) {
	retrying := notification("error", map[string]any{"willRetry": true, "error": map[string]any{"message": "transient"}})
	peer := newCompactPeer(t, answerCompaction(retrying, completedTurn()))
	if _, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
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

func TestCompactFailsWhenTheServerHangsUp(t *testing.T) {
	peer := newCompactPeer(t, func(map[string]any) []map[string]any { return nil })
	go func() {
		<-peer.requests
		peer.hangUp()
	}()
	if _, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil); err == nil {
		t.Fatal("Compact succeeded although the server hung up before the compaction ended")
	}
}

func TestManifestDeclaresTheCompactor(t *testing.T) {
	if !backendPlugin(t).Manifest.Capabilities.Compactor {
		t.Fatal("the codex manifest does not declare Capabilities.Compactor")
	}
}
