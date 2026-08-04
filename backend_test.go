package codex

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

func TestNewBackendSelection(t *testing.T) {
	if _, ok := mustBackend(t, Config{Kind: "oneshot", Cmd: "codex"}).(*oneShotResponder); !ok {
		t.Fatal("oneshot should return oneShotResponder")
	}
	if _, ok := mustBackend(t, Config{Kind: "stream", Cmd: "codex"}).(*streamResponder); !ok {
		t.Fatal("stream should return streamResponder")
	}
}

func TestNewBackendOneshotRequiresCmd(t *testing.T) {
	if _, err := NewBackend(context.Background(), Config{Kind: "oneshot"}); err == nil {
		t.Fatal("empty oneshot command should fail")
	}
}

func TestNewBackendRejectsUnknownKind(t *testing.T) {
	if _, err := NewBackend(context.Background(), Config{Kind: "interactive", Cmd: "codex"}); err == nil {
		t.Fatal("unknown backend kind should fail")
	}
}

func TestParseExecOutput(t *testing.T) {
	out := "{\"type\":\"thread.started\",\"thread_id\":\"t\"}\n" +
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"final answer\"}}\n" +
		"{\"type\":\"turn.completed\"}\n"
	if got := parseExecOutput(out); got != "final answer" {
		t.Fatalf("output=%q", got)
	}
}

func TestParseExecOutputUsesCodexAgentMessageType(t *testing.T) {
	out := "{\"type\":\"item.completed\",\"item\":{\"type\":\"agentMessage\",\"text\":\"actual Codex response\"}}\n"
	if got := parseExecOutput(out); got != "actual Codex response" {
		t.Fatalf("output=%q", got)
	}
}

func TestExecPromptUsesStdinForMultilineContent(t *testing.T) {
	content := "<memory>\nprevious turns\n</memory>\n\ncurrent request"
	arg, stdin := execPrompt(content)
	if arg != "-" {
		t.Fatalf("prompt argument=%q want=-", arg)
	}
	if stdin != content {
		t.Fatalf("stdin=%q want=%q", stdin, content)
	}
}

func TestExecArgsApproveOnlyNeubloxMCPForNonInteractiveTurns(t *testing.T) {
	got := execArgs(
		[]string{"codex", "--profile", "dev"},
		"gpt-5.6-terra",
		"medium",
	)
	want := []string{
		"--profile", "dev",
		"exec", "--json",
		"-c", `approval_policy="never"`,
		"-c", `sandbox_mode="workspace-write"`,
		"-c", `mcp_servers.neublox.default_tools_approval_mode="approve"`,
		"--model", "gpt-5.6-terra",
		"-c", "model_reasoning_effort=medium",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exec args = %v, want %v", got, want)
	}
}

func mustBackend(t *testing.T, c Config) contracts.Backend {
	t.Helper()
	b, err := NewBackend(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRunCmdErrorIncludesStderr pins that a failing codex CLI surfaces its own
// diagnostic. cmd.Output() only populates ExitError.Stderr when Stderr is nil,
// and we set it (for the verbose passthrough), so it must be captured by hand.
func TestRunCmdErrorIncludesStderr(t *testing.T) {
	// A stand-in for the codex binary: ignores its argv, complains on stderr and
	// exits non-zero — exactly the shape that used to debug blind.
	fake := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\necho 'codex: model not found' >&2\nexit 7\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := runCmd(context.Background(), fake, "", "", "", false, nil,
		contracts.Prompt{Content: "unused"})
	if err == nil {
		t.Fatal("expected an error from a failing command")
	}
	if !strings.Contains(err.Error(), "codex exec failed") {
		t.Fatalf("error = %q, want it to mention codex exec failed", err)
	}
	if !strings.Contains(err.Error(), "codex: model not found") {
		t.Fatalf("error = %q, want it to carry the child's stderr", err)
	}
}

func TestRunCmdErrorCarriesChildStderrText(t *testing.T) {
	// Direct check of the capture wiring runCmd uses: stderr must survive
	// cmd.Output(), which discards ExitError.Stderr once Stderr is non-nil.
	buf := &boundedBuffer{limit: stderrCaptureLimit}
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "echo boom-sentinel >&2; exit 3")
	cmd.Stderr = buf
	if _, err := cmd.Output(); err == nil {
		t.Fatal("expected non-zero exit")
	}
	if got := strings.TrimSpace(buf.String()); got != "boom-sentinel" {
		t.Fatalf("captured stderr = %q, want boom-sentinel", got)
	}
}

func TestBoundedBufferTruncatesRunawayStderr(t *testing.T) {
	b := &boundedBuffer{limit: 8}
	n, err := b.Write([]byte("aaaa"))
	if n != 4 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	// Over-long writes must still report a full write so the child never sees a
	// short write on its stderr pipe.
	if n, err := b.Write([]byte("bbbbbbbbbb")); n != 10 || err != nil {
		t.Fatalf("Write = %d, %v; want 10, nil", n, err)
	}
	got := b.String()
	if !strings.HasPrefix(got, "aaaabbbb") {
		t.Fatalf("String() = %q, want it to start with the first 8 bytes", got)
	}
	if !strings.Contains(got, "6 more bytes truncated") {
		t.Fatalf("String() = %q, want a truncation marker", got)
	}
}

func TestRunCmdVerboseStillPassesStderrThrough(t *testing.T) {
	// Verbose keeps the os.Stderr passthrough *and* captures; assert the
	// capturing half via a MultiWriter equivalent.
	var mirror strings.Builder
	buf := &boundedBuffer{limit: stderrCaptureLimit}
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "echo mirrored >&2; exit 1")
	cmd.Stderr = io.MultiWriter(&mirror, buf)
	if _, err := cmd.Output(); err == nil {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(mirror.String(), "mirrored") || !strings.Contains(buf.String(), "mirrored") {
		t.Fatalf("mirror = %q, buf = %q; both should see stderr", mirror.String(), buf.String())
	}
}
