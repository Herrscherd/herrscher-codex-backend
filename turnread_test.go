package codex

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

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

func TestRequestWithoutTurnIdIsServedByTheReadingTurn(t *testing.T) {
	lines := `{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","itemId":"item-1","command":"ls"}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("the reading turn's asker was called %d times, want 1", len(seen))
	}
	if got := onlyDecision(t, sent); got != "accept" {
		t.Fatalf("decision = %q, want accept", got)
	}
}
