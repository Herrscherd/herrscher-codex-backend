package codex

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

func TestParseCompactTimeout(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "unset keeps the default", raw: ""},
		{name: "a plain duration", raw: "15m", want: 15 * time.Minute},
		{name: "a fractional duration", raw: "1.5h", want: 90 * time.Minute},
		{name: "zero is refused", raw: "0", wantErr: true},
		{name: "negative is refused", raw: "-5m", wantErr: true},
		{name: "a word is refused", raw: "soon", wantErr: true},
		{name: "a bare number without a unit is refused", raw: "600", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCompactTimeout(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseCompactTimeout(%q) = %v, want error %t", tc.raw, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "CODEX_COMPACT_TIMEOUT") {
				t.Fatalf("error %q does not name CODEX_COMPACT_TIMEOUT", err)
			}
			if got != tc.want {
				t.Fatalf("parseCompactTimeout(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func resolveCodexSettings(t *testing.T, env map[string]string) contracts.PluginConfig {
	t.Helper()
	cfg, err := contracts.Resolve(backendPlugin(t).Manifest.Config, func(name string) string { return env[name] })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return cfg
}

func TestCompactTimeoutFromTheEnvironmentReachesTheResponder(t *testing.T) {
	plugin := backendPlugin(t)
	cfg := resolveCodexSettings(t, map[string]string{"CODEX_COMPACT_TIMEOUT": "15m"})
	b, err := plugin.Backend(context.Background(), cfg)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	responder, ok := b.(*streamResponder)
	if !ok {
		t.Fatalf("factory returned %T, want the stream responder", b)
	}
	if responder.compactTimeout != 15*time.Minute {
		t.Fatalf("compactTimeout = %v, want 15m from CODEX_COMPACT_TIMEOUT", responder.compactTimeout)
	}
}

func TestFactoryRefusesAnInvalidCompactTimeoutFromTheEnvironment(t *testing.T) {
	plugin := backendPlugin(t)
	for _, raw := range []string{"soon", "0", "-5m"} {
		cfg := resolveCodexSettings(t, map[string]string{"CODEX_COMPACT_TIMEOUT": raw})
		_, err := plugin.Backend(context.Background(), cfg)
		if err == nil || !strings.Contains(err.Error(), "CODEX_COMPACT_TIMEOUT") {
			t.Fatalf("CODEX_COMPACT_TIMEOUT=%q: factory error = %v, want a refusal naming CODEX_COMPACT_TIMEOUT", raw, err)
		}
	}
}

func TestNewBackendCarriesTheConfiguredCompactTimeout(t *testing.T) {
	b, err := NewBackend(context.Background(), Config{Kind: "stream", CompactTimeout: 15 * time.Minute})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	responder, ok := b.(*streamResponder)
	if !ok {
		t.Fatalf("NewBackend returned %T, want the stream responder", b)
	}
	if responder.compactTimeout != 15*time.Minute {
		t.Fatalf("compactTimeout = %v, want 15m", responder.compactTimeout)
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

func TestCompactedResultCarriesTheCompactedNotice(t *testing.T) {
	peer := newCompactPeer(t, answerCompaction(completedTurn()))
	got, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got.Notice.Code != contracts.CodeCompacted || len(got.Notice.Args) != 0 {
		t.Fatalf("notice=%+v, want CodeCompacted without arguments", got.Notice)
	}
}

func TestCompactedNoteIsEnglish(t *testing.T) {
	peer := newCompactPeer(t, answerCompaction(completedTurn()))
	got, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	const want = "Context compacted by Codex, size after compaction not measured."
	if got.Note != want {
		t.Fatalf("note=%q, want %q", got.Note, want)
	}
}

func TestUnavailableReasonIsTheFirstLineBoundedTo200Runes(t *testing.T) {
	long := strings.Repeat("a", 300)
	exact := strings.Repeat("é", 200)
	cases := []struct {
		name   string
		cause  string
		reason string
	}{
		{"several lines keep the first", "first line\nsecond line\nthird", "first line"},
		{"a leading blank line is skipped", "\nfoo", "foo"},
		{"blank lines and spaces are skipped", "  \n\t\n  bar  \nbaz", "bar"},
		{"a long line is cut with an ellipsis", long, strings.Repeat("a", 199) + "…"},
		{"exactly 200 runes is kept whole", exact, exact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &compactUnavailableError{reason: tc.cause}
			notice := err.Notice()
			if notice.Code != contracts.CodeCompactUnavailable || len(notice.Args) != 1 {
				t.Fatalf("notice=%+v, want compact.unavailable with one argument", notice)
			}
			if got := fmt.Sprint(notice.Args[0].Value()); got != tc.reason {
				t.Fatalf("Reason=%q, want %q", got, tc.reason)
			}
			if !strings.Contains(err.Error(), tc.cause) {
				t.Fatalf("Error() = %q, want the full cause kept", err.Error())
			}
		})
	}
}

func requireUnavailable(t *testing.T, err, cause error, reason string) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("err=%v, want %v in its chain", err, cause)
	}
	notice := contracts.NoticeOf(err)
	if notice.Code != contracts.CodeCompactUnavailable {
		t.Fatalf("code=%v, want CodeCompactUnavailable", notice.Code)
	}
	if len(notice.Args) != 1 || notice.Args[0].Name() != "Reason" || notice.Args[0].Kind() != contracts.ArgText {
		t.Fatalf("args=%+v, want one text argument named Reason", notice.Args)
	}
	if notice.Args[0].Value() != reason {
		t.Fatalf("Reason=%q, want %q", notice.Args[0].Value(), reason)
	}
}

func TestUnavailableNoticesCarryTheFixedReason(t *testing.T) {
	_, timeoutErr := parseCompactTimeout("0")
	cases := []struct {
		name   string
		err    error
		reason string
	}{
		{"instruction", errCompactInstruction, "thread/compact/start takes no instruction, and the backend has no field for one"},
		{"no thread", errCompactNoThread, "no turn has run yet, so there is no thread to compact"},
		{"timeout not positive", timeoutErr, "a compaction timeout must be a positive duration"},
	}
	renderer := contracts.NewRenderer()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireUnavailable(t, tc.err, tc.err, tc.reason)
			if _, err := renderer.Render(i18n.French, contracts.NoticeOf(tc.err)); err != nil {
				t.Fatalf("Render: %v", err)
			}
		})
	}
}

func TestCompactRejectionAndFailureReachTheUserAsUnavailable(t *testing.T) {
	cases := []struct {
		name   string
		peer   func(map[string]any) []map[string]any
		cause  error
		reason string
	}{
		{"rejected request", func(request map[string]any) []map[string]any {
			return []map[string]any{{"id": request["id"], "error": map[string]any{"message": "compaction refused"}}}
		}, errCompactionRejected, "the compaction was rejected: compaction refused"},
		{"failed turn", answerCompaction(turnEnded("failed", map[string]any{fieldMessage: "context overflow"})), errCompactionFailed, "the compaction turn failed: context overflow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newCompactPeer(t, tc.peer)
			_, err := compactingResponder(peer).Compact(t.Context(), contracts.CompactRequest{}, nil)
			requireUnavailable(t, err, tc.cause, tc.reason)
		})
	}
}

func TestCompactReopenFailureIsUnavailableAndKeepsItsCause(t *testing.T) {
	errOpen := errors.New("app-server did not start")
	r := &streamResponder{resumeID: "thread-1", start: func(context.Context, string) (*appSession, error) {
		return nil, errOpen
	}}
	_, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
	requireUnavailable(t, err, errOpen, "reopening the thread: app-server did not start")
}

func TestCompactDeadlineIsUnavailableAndKeepsItsCause(t *testing.T) {
	peer := newCompactPeer(t, answerCompaction(completedTurn()))
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := compactingResponder(peer).Compact(ctx, contracts.CompactRequest{}, nil)
	requireUnavailable(t, err, context.DeadlineExceeded, "the compaction failed: context deadline exceeded")
}

type brokenWriteCloser struct {
	err error
}

func (w brokenWriteCloser) Write([]byte) (int, error) { return 0, w.err }

func (w brokenWriteCloser) Close() error { return nil }

type silentWriteCloser struct{}

func (silentWriteCloser) Write(p []byte) (int, error) { return len(p), nil }

func (silentWriteCloser) Close() error { return nil }

type brokenReader struct {
	err error
}

func (r brokenReader) Read([]byte) (int, error) { return 0, r.err }

func TestCompactWriteFailureIsUnavailableAndKeepsItsCause(t *testing.T) {
	errWrite := errors.New("pipe to codex closed")
	r := &streamResponder{sess: newAppSession(brokenWriteCloser{err: errWrite}, strings.NewReader(""))}
	_, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
	requireUnavailable(t, err, errWrite, "the compaction failed: pipe to codex closed")
}

func TestCompactReadFailureIsUnavailableAndKeepsItsCause(t *testing.T) {
	errRead := errors.New("codex stdout closed")
	r := &streamResponder{sess: newAppSession(silentWriteCloser{}, brokenReader{err: errRead})}
	_, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
	requireUnavailable(t, err, errRead, "the compaction failed: codex: app-server closed before any turn output")
}
