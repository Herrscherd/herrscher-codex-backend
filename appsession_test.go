package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

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
