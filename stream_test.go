package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
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
	go func() { replies <- replyOnce(inR, outW, "thread-1") }()
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
	go func() { done <- s.initializeWithinTurn(ctx, "") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initializeWithinTurn err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initializeWithinTurn did not honour a cancelled ctx")
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
		go func() { replies <- replyOnce(inR, outW, "thread-2") }()
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

func replyOnce(in io.Reader, out *io.PipeWriter, thread string) error {
	if _, err := bufio.NewReader(in).ReadBytes('\n'); err != nil {
		return out.Close()
	}
	_, err := io.WriteString(out, `{"method":"item/agentMessage/delta","params":{"threadId":"`+thread+`","delta":"ok"}}`+"\n"+`{"method":"turn/started","params":{"threadId":"`+thread+`","turn":{"id":"turn-1"}}}`+"\n"+`{"method":"turn/completed","params":{"threadId":"`+thread+`","turn":{"id":"turn-1","status":"completed"}}}`+"\n")
	return errors.Join(err, out.Close())
}

func closeChecked(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestStopProcessReportsAWaitFailureOtherThanAnExit(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	session := &appSession{cmd: cmd}
	if err := session.stopProcess(); err == nil {
		t.Fatal("stopProcess hid a Wait failure that is not an exit status")
	}
}

func TestStopProcessIgnoresTheExitStatusOfTheChild(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 3")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	session := &appSession{cmd: cmd}
	if err := session.stopProcess(); err != nil {
		t.Fatalf("stopProcess = %v, want nil for a child stopped on Close", err)
	}
}

const (
	quietPeriod   = 100 * time.Millisecond
	writeDeadline = 5 * time.Second
)

type turnProbe struct {
	mu      sync.Mutex
	calls   int
	writes  chan struct{}
	release chan struct{}
	forward io.Writer
}

func (p *turnProbe) Write(data []byte) (int, error) {
	p.mu.Lock()
	p.calls++
	first := p.calls == 1
	p.mu.Unlock()
	p.writes <- struct{}{}
	if first {
		<-p.release
	}
	return p.forward.Write(data)
}

func (p *turnProbe) Close() error { return nil }

func serveTurns(in io.Reader, out *io.PipeWriter) error {
	reader := bufio.NewReader(in)
	for {
		if _, err := reader.ReadBytes('\n'); err != nil {
			return out.Close()
		}
		reply := `{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","delta":"ok"}}` + "\n" +
			`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-1"}}}` + "\n" + `{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}` + "\n"
		if _, err := io.WriteString(out, reply); err != nil {
			return errors.Join(err, out.Close())
		}
	}
}

func servedPipe(t *testing.T) (*io.PipeWriter, *io.PipeReader) {
	t.Helper()
	reqR, reqW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- serveTurns(reqR, outW) }()
	t.Cleanup(func() {
		closeChecked(t, reqW)
		if err := <-served; err != nil {
			t.Errorf("serve turns: %v", err)
		}
	})
	return reqW, outR
}

func TestSendHoldsTheSessionForOneTurn(t *testing.T) {
	reqW, outR := servedPipe(t)
	probe := &turnProbe{writes: make(chan struct{}, 4), release: make(chan struct{}), forward: reqW}
	s := newAppSession(probe, outR)
	s.threadID = "thread-1"

	first := make(chan error, 1)
	go func() {
		_, err := s.Send(context.Background(), "one", nil)
		first <- err
	}()
	<-probe.writes

	second := make(chan error, 1)
	go func() {
		_, err := s.Send(context.Background(), "two", nil)
		second <- err
	}()
	select {
	case <-probe.writes:
		t.Error("a second turn was written while the first was in flight")
	case <-time.After(quietPeriod):
	}
	close(probe.release)
	select {
	case <-probe.writes:
	case <-time.After(writeDeadline):
		t.Error("the second turn was never written")
	}

	if err := <-first; err != nil {
		t.Errorf("first Send: %v", err)
	}
	if err := <-second; err != nil {
		t.Errorf("second Send: %v", err)
	}
}

func TestRespondOpensOneSessionForConcurrentTurns(t *testing.T) {
	var opened atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	r := &streamResponder{}
	r.start = func(context.Context, string) (*appSession, error) {
		opened.Add(1)
		entered <- struct{}{}
		<-release
		reqW, outR := servedPipe(t)
		s := newAppSession(reqW, outR)
		s.threadID = "thread-1"
		return s, nil
	}
	prompt := contracts.Prompt{Content: "hi"}

	first := make(chan error, 1)
	go func() {
		_, err := r.Respond(context.Background(), prompt, nil)
		first <- err
	}()
	<-entered

	second := make(chan error, 1)
	go func() {
		_, err := r.Respond(context.Background(), prompt, nil)
		second <- err
	}()
	select {
	case <-entered:
		t.Error("a second Respond opened a session while the first was opening one")
	case <-time.After(quietPeriod):
	}
	close(release)

	if err := <-first; err != nil {
		t.Errorf("first Respond: %v", err)
	}
	if err := <-second; err != nil {
		t.Errorf("second Respond: %v", err)
	}
	if n := opened.Load(); n != 1 {
		t.Errorf("sessions opened = %d, want 1", n)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func agentText(turnID, text string) map[string]any {
	return notification("item/completed", map[string]any{fieldThreadID: "thread-1", fieldTurnID: turnID, fieldItem: map[string]any{fieldType: "agentMessage", fieldText: text}})
}

func respondWithin(t *testing.T, r *streamResponder, content string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.Respond(ctx, contracts.Prompt{Content: content}, nil)
}

func TestRespondReadsTheTurnToItsEndAfterAnErrorNotice(t *testing.T) {
	turns := 0
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != "turn/start" {
			return nil
		}
		turns++
		if turns == 1 {
			return []map[string]any{turnStartedNotice("turn-1"), errorNotice(false, "boom"), turnEndedAs("turn-1", "failed", nil)}
		}
		return []map[string]any{turnStartedNotice("turn-2"), agentText("turn-2", "deux"), turnEndedAs("turn-2", "completed", nil)}
	})
	r := compactingResponder(peer)
	if _, err := respondWithin(t, r, "un"); !errors.Is(err, errTurnFailed) {
		t.Fatalf("first turn err=%v, want errTurnFailed", err)
	}
	peer.requireWritten(t, "turn/completed")
	got, err := respondWithin(t, r, "deux")
	if err != nil || got != "deux" {
		t.Fatalf("second turn got %q, %v; want its own answer", got, err)
	}
}

func TestRespondIgnoresACompletionOfAnotherTurn(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != "turn/start" {
			return nil
		}
		return []map[string]any{
			turnStartedNotice("turn-2"),
			turnEndedAs("turn-1", "failed", nil),
			agentText("turn-2", "réponse"),
			agentText("turn-1", "intrus"),
			turnEndedAs("turn-2", "completed", nil),
		}
	})
	r := compactingResponder(peer)
	got, err := respondWithin(t, r, "un")
	if err != nil || got != "réponse" {
		t.Fatalf("got %q, %v; want the answer of its own turn", got, err)
	}
}

func TestRespondIgnoresACompletionBeforeItsTurnStarts(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != "turn/start" {
			return nil
		}
		return []map[string]any{
			turnEndedAs("turn-0", "completed", nil),
			turnStartedNotice("turn-1"),
			agentText("turn-1", "réponse"),
			turnEndedAs("turn-1", "completed", nil),
		}
	})
	r := compactingResponder(peer)
	got, err := respondWithin(t, r, "un")
	if err != nil || got != "réponse" {
		t.Fatalf("got %q, %v; want the answer of the turn that was started", got, err)
	}
}

func TestRespondEndsOnTheCompletionOfItsResponseIdWithoutTurnStarted(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != "turn/start" {
			return nil
		}
		return []map[string]any{turnStartReply(request), agentText("turn-1", "réponse"), turnEndedAs("turn-1", "completed", nil)}
	})
	got, err := respondWithin(t, compactingResponder(peer), "un")
	if err != nil || got != "réponse" {
		t.Fatalf("got %q, %v; want the answer of the turn named by its response", got, err)
	}
}

func TestRespondIgnoresACompletionOfAnotherIdAfterItsResponse(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != "turn/start" {
			return nil
		}
		return []map[string]any{turnStartReply(request), turnEndedAs("turn-2", "failed", nil), agentText("turn-1", "réponse"), turnEndedAs("turn-1", "completed", nil)}
	})
	got, err := respondWithin(t, compactingResponder(peer), "un")
	if err != nil || got != "réponse" {
		t.Fatalf("got %q, %v; want the answer of the turn named by its response", got, err)
	}
}

func TestRespondTakesACompletionThatArrivesBeforeItsResponse(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != "turn/start" {
			return nil
		}
		return []map[string]any{
			agentText("turn-1", "réponse"),
			turnEndedAs("turn-1", "completed", nil),
			turnStartReply(request),
		}
	})
	got, err := respondWithin(t, compactingResponder(peer), "un")
	if err != nil || got != "réponse" {
		t.Fatalf("got %q, %v; want the answer of turn-1 once its response names it", got, err)
	}
}

func TestRespondFailsWhenItsResponseAndTurnStartedNameTwoIds(t *testing.T) {
	orders := map[string]func(map[string]any) []map[string]any{
		"response first": func(request map[string]any) []map[string]any {
			return []map[string]any{turnStartReply(request), turnStartedNotice("turn-2")}
		},
		"turn/started first": func(request map[string]any) []map[string]any {
			return []map[string]any{turnStartedNotice("turn-2"), turnStartReply(request)}
		},
	}
	for name, reply := range orders {
		t.Run(name, func(t *testing.T) {
			peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
				if request[fieldMethod] != "turn/start" {
					return nil
				}
				return reply(request)
			})
			r := compactingResponder(peer)
			_, err := respondWithin(t, r, "un")
			if !errors.Is(err, errTurnIDMismatch) || !strings.Contains(err.Error(), "turn-1") || !strings.Contains(err.Error(), "turn-2") {
				t.Fatalf("err=%v, want errTurnIDMismatch naming turn-1 and turn-2 without waiting for the context", err)
			}
			if r.sess != nil {
				t.Fatal("the session that named two ids for one turn was kept")
			}
		})
	}
}

func earlyCompletions(count int) []map[string]any {
	completions := make([]map[string]any, 0, count)
	for i := 2; i < 2+count; i++ {
		completions = append(completions, turnEndedAs(fmt.Sprintf("turn-%d", i), "completed", nil))
	}
	return completions
}

func TestRespondAcceptsSixteenCompletionsBeforeItsResponse(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != "turn/start" {
			return nil
		}
		messages := []map[string]any{turnEndedAs("turn-1", "completed", nil)}
		messages = append(messages, earlyCompletions(15)...)
		messages = append(messages, agentText("turn-1", "réponse"), turnStartReply(request))
		return messages
	})
	got, err := respondWithin(t, compactingResponder(peer), "un")
	if err != nil || got != "réponse" {
		t.Fatalf("got %q, %v; want turn-1 answered once its response names it", got, err)
	}
}

func TestRespondFailsWhenSeventeenCompletionsEndBeforeItsResponse(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != "turn/start" {
			return nil
		}
		messages := []map[string]any{turnEndedAs("turn-1", "completed", nil)}
		messages = append(messages, earlyCompletions(16)...)
		return append(messages, turnStartReply(request))
	})
	r := compactingResponder(peer)
	_, err := respondWithin(t, r, "un")
	if !errors.Is(err, errParkedFull) || !strings.Contains(err.Error(), "turn-17") {
		t.Fatalf("err=%v, want errParkedFull naming turn-17 without waiting for the context", err)
	}
	if r.sess != nil {
		t.Fatal("the session that dropped an unnamed completion was kept")
	}
}

func TestRespondReturnsItsTextWithoutAnEventSink(t *testing.T) {
	peer := newCompactPeer(t, func(request map[string]any) []map[string]any {
		if request[fieldMethod] != "turn/start" {
			return nil
		}
		return []map[string]any{turnStartedNotice("turn-1"), agentText("turn-1", "texte"), turnEndedAs("turn-1", "completed", nil)}
	})
	got, err := respondWithin(t, compactingResponder(peer), "un")
	if err != nil || got != "texte" {
		t.Fatalf("got %q, %v; want the text without a sink", got, err)
	}
}
