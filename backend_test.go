package codex

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
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

func TestRunCmdSendsThePromptOnStdin(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	argFile := filepath.Join(dir, "arg")
	stdinFile := filepath.Join(dir, "stdin")
	script := "#!/bin/sh\nfor last; do :; done\nprintf '%s' \"$last\" > " + argFile + "\ncat > " + stdinFile + "\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "<memory>\nprevious turns\n</memory>\n\ncurrent request"
	if _, err := runCmd(context.Background(), fake, "", "", "", false, nil, contracts.Prompt{Content: content}); err != nil {
		t.Fatal(err)
	}
	arg, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(arg) != stdinPromptArg {
		t.Fatalf("prompt argument=%q want=%q", arg, stdinPromptArg)
	}
	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != content {
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
	if !slices.Equal(got, want) {
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

func TestRunCmdErrorIncludesStderr(t *testing.T) {
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
	failure, ok := errors.AsType[*execError](err)
	if !ok {
		t.Fatalf("err = %v, want an *execError", err)
	}
	if failure.stderr != "codex: model not found" {
		t.Fatalf("stderr = %q, want the child's stderr", failure.stderr)
	}
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exitErr.ExitCode() != 7 {
		t.Fatalf("err = %v, want the child's exit status 7", err)
	}
}

func TestRunCmdErrorCarriesChildStderrText(t *testing.T) {
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

func TestParseExecOutputPrefersAgentMessageOverATrailingFooter(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "trailing footer after the agent message",
			out: "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"final answer\"}}\n" +
				"{\"type\":\"turn.completed\"}\n" +
				"tokens used: 12043\n",
			want: "final answer",
		},
		{
			name: "plain text only",
			out:  "tokens used: 12043\r\n",
			want: "tokens used: 12043",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseExecOutput(tc.out); got != tc.want {
				t.Fatalf("output=%q want=%q", got, tc.want)
			}
		})
	}
}
