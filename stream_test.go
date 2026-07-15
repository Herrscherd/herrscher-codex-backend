package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

func TestAppServerArgv(t *testing.T) {
	got := appServerArgv([]string{"codex", "--profile", "dev"})
	want := []string{"codex", "--profile", "dev", "app-server", "--listen", "stdio://"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("argv=%v want=%v", got, want)
	}
}

func TestReadTurnMapsCodexEvents(t *testing.T) {
	lines := strings.Join([]string{
		`{"method":"item/agentMessage/delta","params":{"delta":"hello"}}`,
		`{"method":"item/started","params":{"item":{"type":"command_execution","command":"go test ./..."}}}`,
		`{"method":"item/completed","params":{"item":{"type":"agent_message","text":"hello world"}}}`,
		`{"method":"turn/completed","params":{"turn":{"status":"completed"}}}`,
	}, "\n") + "\n"
	var got []contracts.BackendEvent
	tr, err := readTurn(bufio.NewReader(strings.NewReader(lines)), func(e contracts.BackendEvent) { got = append(got, e) })
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
		`{"method":"turn/completed","params":{"turn":{"status":"completed"}}}`,
	}, "\n") + "\n"
	var got []contracts.BackendEvent
	tr, err := readTurn(bufio.NewReader(strings.NewReader(lines)), func(e contracts.BackendEvent) { got = append(got, e) })
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
		`{"method":"turn/completed","params":{"turn":{"status":"completed"}}}` + "\n"
	if _, err := readTurn(bufio.NewReader(strings.NewReader(input)), nil); err != nil {
		t.Fatal(err)
	}
}

func TestReadTurnReturnsJSONRPCError(t *testing.T) {
	input := `{"id":3,"error":{"message":"turn rejected"}}` + "\n"
	tr, err := readTurn(bufio.NewReader(strings.NewReader(input)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.IsError || tr.ErrMsg != "turn rejected" {
		t.Fatalf("turn=%+v", tr)
	}
}

func TestRequestShapes(t *testing.T) {
	for _, msg := range []map[string]any{
		initializeRequest(1),
		threadStartRequest(2, "gpt-5.4", "/tmp", ""),
		turnStartRequest(3, "thread-1", "hello", "gpt-5.4", "high"),
	} {
		if _, err := json.Marshal(msg); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := initializeRequest(1)["method"]; !ok {
		t.Fatal("initialize request missing method")
	}
}

func TestAppSessionSend(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		defer outW.Close()
		br := bufio.NewReader(inR)
		if _, err := br.ReadBytes('\n'); err != nil {
			return
		}
		io.WriteString(outW, `{"method":"item/agentMessage/delta","params":{"delta":"ok"}}`+"\n")
		io.WriteString(outW, `{"method":"turn/completed","params":{"turn":{"status":"completed"}}}`+"\n")
	}()
	s := newAppSession(inW, outR)
	s.threadID = "thread-1"
	tr, err := s.Send("hi", nil)
	if err != nil || tr.Text != "ok" {
		t.Fatalf("turn=%+v err=%v", tr, err)
	}
}
