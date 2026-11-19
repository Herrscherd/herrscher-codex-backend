package codex

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestWithAttachmentsMarksEachImageInEnglish(t *testing.T) {
	if got := withAttachments("hello", nil); got != "hello" {
		t.Fatalf("no attachments should pass text through, got %q", got)
	}
	got := withAttachments("look", []string{"/tmp/a.png", "/tmp/b.png"})
	want := "look\n\n[Attached image: /tmp/a.png]\n[Attached image: /tmp/b.png]"
	if got != want {
		t.Fatalf("withAttachments = %q, want %q", got, want)
	}
}
