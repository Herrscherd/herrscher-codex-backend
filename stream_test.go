package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestAppServerArgv(t *testing.T) {
	got := appServerArgv([]string{"codex", "--profile", "dev"}, false)
	want := []string{
		"codex", "--profile", "dev",
		"-c", `approval_policy="never"`,
		"-c", `sandbox_mode="workspace-write"`,
		"-c", `mcp_servers.neublox.default_tools_approval_mode="approve"`,
		"app-server", "--listen", "stdio://",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("argv=%v want=%v", got, want)
	}
}

func TestNewBackendExtractsModelAndEffortFromStreamCommand(t *testing.T) {
	backend, err := NewBackend(context.Background(), Config{
		Kind: "stream",
		Cmd:  "codex --model gpt-5-codex --effort high",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := backend.(*streamResponder)
	if !ok {
		t.Fatalf("backend is %T, want *streamResponder", backend)
	}
	if strings.Join(got.base, " ") != "codex" {
		t.Fatalf("base=%v want=[codex]", got.base)
	}
	if got.model != "gpt-5-codex" {
		t.Fatalf("model=%q want=%q", got.model, "gpt-5-codex")
	}
	if got.effort != "high" {
		t.Fatalf("effort=%q want=%q", got.effort, "high")
	}
}

func TestStreamCommandOverridesGlobalModelAndEffort(t *testing.T) {
	backend, err := NewBackend(context.Background(), Config{
		Kind:   "stream",
		Cmd:    "codex --model gpt-5.6-terra --effort medium",
		Model:  "gpt-5-codex",
		Effort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := backend.(*streamResponder)
	if !ok {
		t.Fatalf("backend is %T, want *streamResponder", backend)
	}
	if got.model != "gpt-5.6-terra" {
		t.Fatalf("model=%q want=%q", got.model, "gpt-5.6-terra")
	}
	if got.effort != "medium" {
		t.Fatalf("effort=%q want=%q", got.effort, "medium")
	}
}

func TestOneShotCommandExtractsTurnConfiguration(t *testing.T) {
	spec := commandOverrides("codex --model gpt-5.6-terra --effort medium", "", "")
	if strings.Join(spec.argv, " ") != "codex" {
		t.Fatalf("argv=%v want=[codex]", spec.argv)
	}
	if spec.model != "gpt-5.6-terra" {
		t.Fatalf("model=%q", spec.model)
	}
	if spec.effort != "medium" {
		t.Fatalf("effort=%q", spec.effort)
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
	tr, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(lines)), func(e contracts.BackendEvent) { got = append(got, e) }, nil, nil)
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
	tr, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(lines)), func(e contracts.BackendEvent) { got = append(got, e) }, nil, nil)
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
	if _, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(input)), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestReadTurnReturnsJSONRPCError(t *testing.T) {
	input := `{"id":3,"error":{"message":"turn rejected"}}` + "\n"
	tr, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(input)), nil, nil, nil)
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
	replies := make(chan error, 1)
	go func() { replies <- replyOnce(inR, outW) }()
	s := newAppSession(inW, outR)
	s.threadID = "thread-1"
	tr, err := s.Send(context.Background(), "hi", nil)
	if err != nil || tr.Text != "ok" {
		t.Fatalf("turn=%+v err=%v", tr, err)
	}
	if err := <-replies; err != nil {
		t.Fatal(err)
	}
}

func TestAppSessionSendHonorsCanceledContext(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer closeChecked(t, inR)
	defer closeChecked(t, outR)
	defer closeChecked(t, outW)
	s := newAppSession(inW, outR)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Send(ctx, "never send", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

func TestInitializeCtxAbortsOnCancel(t *testing.T) {
	inR, inW := io.Pipe()
	drained := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, inR)
		drained <- err
	}()
	t.Cleanup(func() {
		closeChecked(t, inW)
		if err := <-drained; err != nil {
			t.Errorf("drain stdin: %v", err)
		}
	})
	stdoutR, stdoutW := io.Pipe()
	defer closeChecked(t, stdoutW)
	s := newAppSession(inW, stdoutR)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- s.initializeCtx(ctx, "") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initializeCtx err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initializeCtx did not honour a cancelled ctx")
	}
}

func TestAppSessionSendWaitsForItsReaderOnCancel(t *testing.T) {
	stdout := newBlockingReader()
	s := newAppSession(nopWriteCloser{io.Discard}, stdout)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Send(ctx, "hi", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want context.DeadlineExceeded", err)
	}
	if !stdout.exited.Load() {
		t.Fatal("Send returned while its reader was still running")
	}
}

func TestAppSessionCloseClosesOnceAndReportsItsError(t *testing.T) {
	out := &countingCloser{Reader: strings.NewReader("")}
	s := newAppSession(nopWriteCloser{io.Discard}, out)
	first := s.Close()
	second := s.Close()
	if !errors.Is(first, errCloseFailed) || !errors.Is(second, errCloseFailed) {
		t.Fatalf("Close errors = %v, %v; want errCloseFailed twice", first, second)
	}
	if out.calls != 1 {
		t.Fatalf("underlying Close called %d times, want 1", out.calls)
	}
}

var errCloseFailed = errors.New("close failed")

type countingCloser struct {
	io.Reader
	calls int
}

func (c *countingCloser) Close() error {
	c.calls++
	return errCloseFailed
}

type blockingReader struct {
	closed    chan struct{}
	closeOnce sync.Once
	exited    atomic.Bool
}

func newBlockingReader() *blockingReader {
	return &blockingReader{closed: make(chan struct{})}
}

func (b *blockingReader) Read([]byte) (int, error) {
	<-b.closed
	b.exited.Store(true)
	return 0, io.ErrClosedPipe
}

func (b *blockingReader) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
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
			_, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(tc.stream)), nil, nil, func(map[string]any) error { return nil })
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

func TestRespondReopensTheThreadWhenTheServerDiesBeforeTheTurn(t *testing.T) {
	var resumed []string
	r := &streamResponder{}
	r.start = func(_ context.Context, resume string) (*appSession, error) {
		resumed = append(resumed, resume)
		if len(resumed) == 1 {
			s := newAppSession(nopWriteCloser{io.Discard}, strings.NewReader(""))
			s.threadID = "thread-1"
			return s, nil
		}
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		replies := make(chan error, 1)
		go func() { replies <- replyOnce(inR, outW) }()
		t.Cleanup(func() {
			if err := <-replies; err != nil {
				t.Errorf("reply: %v", err)
			}
		})
		s := newAppSession(inW, outR)
		s.threadID = "thread-2"
		return s, nil
	}
	var events []contracts.BackendEvent
	got, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, func(e contracts.BackendEvent) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Fatalf("text=%q want ok", got)
	}
	if strings.Join(resumed, ",") != ",thread-1" {
		t.Fatalf("starts resumed %q, want the first start then thread-1", resumed)
	}
	if len(events) == 0 || events[0].Kind != "reset" {
		t.Fatalf("events=%+v, want a leading reset", events)
	}
	if r.ResumeToken() != "thread-2" {
		t.Fatalf("token=%q want thread-2", r.ResumeToken())
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func replyOnce(in io.Reader, out *io.PipeWriter) error {
	if _, err := bufio.NewReader(in).ReadBytes('\n'); err != nil {
		return out.Close()
	}
	_, err := io.WriteString(out, `{"method":"item/agentMessage/delta","params":{"delta":"ok"}}`+"\n"+`{"method":"turn/completed","params":{"turn":{"status":"completed"}}}`+"\n")
	return errors.Join(err, out.Close())
}

func closeChecked(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
