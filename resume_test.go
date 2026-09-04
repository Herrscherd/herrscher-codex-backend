package codex

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestStreamResponderResumeToken(t *testing.T) {
	// Before the first turn, the token is whatever was passed in at construction.
	r := &streamResponder{resumeID: "boot-id"}
	if got := r.ResumeToken(); got != "boot-id" {
		t.Fatalf("nil session: want boot-id, got %q", got)
	}
	// Once a session exists, the live codex thread id wins.
	r.sess = newAppSession(nopWriteCloser{io.Discard}, strings.NewReader(""))
	r.sess.threadID = "thread-1"
	r.rememberToken("thread-1")
	if got := r.ResumeToken(); got != "thread-1" {
		t.Fatalf("live session: want thread-1, got %q", got)
	}
}

func TestStreamResponderIsResumeAware(t *testing.T) {
	var _ contracts.ResumeAware = (*streamResponder)(nil)
}

func TestNewBackendThreadsResumeID(t *testing.T) {
	b, err := NewBackend(context.Background(), Config{Kind: "stream", Cmd: "codex", ResumeID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := b.(*streamResponder)
	if !ok {
		t.Fatalf("want *streamResponder, got %T", b)
	}
	if r.resumeID != "x" {
		t.Fatalf("resumeID not threaded: got %q", r.resumeID)
	}
}

func TestResumeTokenDoesNotWaitForTheTurnLock(t *testing.T) {
	r := &streamResponder{resumeID: "boot-id"}
	r.mu.Lock()
	defer r.mu.Unlock()
	done := make(chan string, 1)
	go func() { done <- r.ResumeToken() }()
	select {
	case got := <-done:
		if got != "boot-id" {
			t.Fatalf("token = %q, want boot-id", got)
		}
	case <-time.After(time.Second):
		t.Fatal("ResumeToken blocked while a turn held the responder lock")
	}
}
