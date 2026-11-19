package codex

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestReadTurnMapsCodexEvents(t *testing.T) {
	lines := strings.Join([]string{
		`{"method":"item/agentMessage/delta","params":{"delta":"hello"}}`,
		`{"method":"item/started","params":{"item":{"type":"command_execution","command":"go test ./..."}}}`,
		`{"method":"item/completed","params":{"item":{"type":"agent_message","text":"hello world"}}}`,
		`{"method":"turn/started","params":{"turn":{"id":"turn-1"}}}` + "\n" + `{"method":"turn/completed","params":{"turn":{"id":"turn-1","status":"completed"}}}`,
	}, "\n") + "\n"
	var got []contracts.BackendEvent
	tr, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(lines)), turnScope{}, func(e contracts.BackendEvent) { got = append(got, e) }, nil, nil)
	if err != nil || tr.Text != "hello world" {
		t.Fatalf("turn=%+v err=%v", tr, err)
	}
	if len(got) != 3 || got[0].Kind != "text" || got[1].Kind != "tool" || got[2].Kind != "result" {
		t.Fatalf("events=%+v", got)
	}
}

func TestReadTurnMapsCodexCamelCaseItems(t *testing.T) {
	lines := strings.Join([]string{
		`{"method":"item/started","params":{"item":{"type":"commandExecution","command":"go test ./..."}}}`,
		`{"method":"item/completed","params":{"item":{"type":"agentMessage","text":"response without delta"}}}`,
		`{"method":"turn/started","params":{"turn":{"id":"turn-1"}}}` + "\n" + `{"method":"turn/completed","params":{"turn":{"id":"turn-1","status":"completed"}}}`,
	}, "\n") + "\n"
	var got []contracts.BackendEvent
	tr, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(lines)), turnScope{}, func(e contracts.BackendEvent) { got = append(got, e) }, nil, nil)
	if err != nil || tr.Text != "response without delta" {
		t.Fatalf("turn=%+v err=%v", tr, err)
	}
	if len(got) != 2 || got[0].Kind != "tool" || got[1].Kind != "result" {
		t.Fatalf("events=%+v", got)
	}
}

func TestReadTurnHandlesHugeLine(t *testing.T) {
	huge := strings.Repeat("x", 200_000)
	input := `{"method":"item/agentMessage/delta","params":{"delta":"` + huge + `"}}` + "\n" +
		`{"method":"turn/started","params":{"turn":{"id":"turn-1"}}}` + "\n" + `{"method":"turn/completed","params":{"turn":{"id":"turn-1","status":"completed"}}}` + "\n"
	if _, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(input)), turnScope{}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestReadTurnReturnsJSONRPCError(t *testing.T) {
	input := `{"id":3,"error":{"message":"turn rejected"}}` + "\n"
	tr, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(input)), turnScope{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.IsError || tr.ErrMsg != "turn rejected" {
		t.Fatalf("turn=%+v", tr)
	}
}

func TestReadTurnDistinguishesAStartedTurnFromASilentServer(t *testing.T) {
	cases := []struct {
		name       string
		stream     string
		notStarted bool
	}{
		{name: "server closed before any output", stream: "", notStarted: true},
		{name: "server died mid turn", stream: `{"method":"item/agentMessage/delta","params":{"delta":"hi"}}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(tc.stream)), turnScope{}, nil, nil, func(map[string]any) error { return nil })
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errors.Is(err, errTurnNotStarted); got != tc.notStarted {
				t.Fatalf("errTurnNotStarted = %v, want %v (err=%v)", got, tc.notStarted, err)
			}
			if !errors.Is(err, io.EOF) {
				t.Fatalf("underlying error lost: %v", err)
			}
		})
	}
}

func TestRequestNamingAnotherTurnIsDeclinedWithoutAskingTheAsker(t *testing.T) {
	lines := `{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-2"}}}
{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-1","command":"ls"}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-2"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("the asker was called %d times for a request of another turn", len(seen))
	}
	if got := onlyDecision(t, sent); got != "decline" {
		t.Fatalf("decision = %q, want decline", got)
	}
}

func TestLeftoverRequestOfAPreviousTurnIsDeclinedForTheReadingTurn(t *testing.T) {
	lines := `{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-2"}}}
{"id":9,"method":"item/fileChange/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-2","fileChanges":{"a.go":{}}}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-2"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("the asker approved a previous turn's request %d times", len(seen))
	}
	if got := onlyDecision(t, sent); got != "decline" {
		t.Fatalf("decision = %q, want decline", got)
	}
}

func TestRequestWithoutTurnIdIsDeclinedWithAWarningAndNoAskerCall(t *testing.T) {
	lines := `{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","itemId":"item-1","command":"ls"}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	var err error
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	warnings := captureStderr(t, func() {
		_, err = readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent))
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("the asker was called %d times for a request without turnId", len(seen))
	}
	if got := onlyDecision(t, sent); got != "decline" {
		t.Fatalf("decision = %q, want decline", got)
	}
	if !strings.Contains(warnings, "item/commandExecution/requestApproval") {
		t.Fatalf("the refusal of a request without turnId was not logged: %q", warnings)
	}
}

func TestParkedRequestIsDeclinedAtEOFWithAReadFailure(t *testing.T) {
	lines := `{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-1","command":"ls"}}` + "\n"
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	_, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want the read failure at EOF", err)
	}
	if len(seen) != 0 {
		t.Fatalf("the asker was called %d times for a parked request", len(seen))
	}
	if got := onlyDecision(t, sent); got != "decline" {
		t.Fatalf("decision = %q, want decline", got)
	}
}

func TestParkedRequestDoesNotBlockAwaitTurnWhenTheContextIsCancelled(t *testing.T) {
	var seen []contracts.ApprovalRequest
	ctx, cancel := context.WithCancel(withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen)))
	defer cancel()
	reader, writer := io.Pipe()
	t.Cleanup(func() { closeLogged(t, writer.Close) })
	session := newAppSession(nopWriteCloser{io.Discard}, reader)
	request := `{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-1","command":"ls"}}` + "\n"
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, request)
		written <- err
	}()
	outcome := make(chan error, 1)
	go func() {
		_, err := session.awaitTurn(ctx, turnScope{threadID: "t1"}, nil)
		outcome <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitTurn never read the request")
	}
	cancel()
	select {
	case err := <-outcome:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want the context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitTurn blocked after the context was cancelled with a request parked")
	}
	if len(seen) != 0 {
		t.Fatalf("the asker was called %d times for a parked request", len(seen))
	}
}

func TestRequestWithoutTurnIdIsDeclinedWithoutAnApproverOrAsker(t *testing.T) {
	lines := `{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","itemId":"item-1","command":"ls"}}
{"id":10,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"","itemId":"item-2","command":"pwd"}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var sent []map[string]any
	var err error
	warnings := captureStderr(t, func() {
		_, err = readTurn(context.Background(), bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent))
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent %d replies, want 2", len(sent))
	}
	for _, reply := range sent {
		if got := mapField(reply, fieldResult)["decision"]; got != "decline" {
			t.Fatalf("decision = %v, want decline", got)
		}
	}
	if n := strings.Count(warnings, "item/commandExecution/requestApproval"); n != 1 {
		t.Fatalf("%d warnings for requests without turnId, want 1: %q", n, warnings)
	}
}

func TestWriteFailureDuringTheFinalDeclineIsJoined(t *testing.T) {
	lines := `{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-1","command":"ls"}}` + "\n"
	session := newAppSession(brokenWriteCloser{err: errNoWrite}, strings.NewReader(""))
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	_, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, session.write)
	if !errors.Is(err, errNoWrite) {
		t.Fatalf("err = %v, want the stdin failure joined to the read failure", err)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want the read failure kept next to the stdin failure", err)
	}
}

func TestParkedRequestIsServedAfterACompactionItemStarts(t *testing.T) {
	lines := `{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-1","command":"ls"}}
{"method":"item/started","params":{"threadId":"t1","turnId":"turn-1","item":{"type":"contextCompaction","id":"c1"}}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1", compaction: true}, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("the asker was called %d times, want 1 once the compaction item named the turn", len(seen))
	}
	if got := onlyDecision(t, sent); got != "accept" {
		t.Fatalf("decision = %q, want accept", got)
	}
}

func TestParkedRequestsAreServedInArrivalOrder(t *testing.T) {
	lines := `{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-1","command":"first"}}
{"id":10,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-2","command":"second"}}
{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0].Subject != "first" || seen[1].Subject != "second" {
		t.Fatalf("the asker saw %v, want first then second", seen)
	}
	if len(sent) != 2 || sent[0]["id"] != float64(9) || sent[1]["id"] != float64(10) {
		t.Fatalf("replies went out in the wrong order: %v", sent)
	}
}
