package codex

import (
	"context"
	"reflect"
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

func TestCommandPresets(t *testing.T) {
	got := CommandPresets("codex")
	if len(got) == 0 {
		t.Fatal("expected Codex command presets")
	}
	if got[0].Value == "" || got[0].Label == "" {
		t.Fatalf("invalid preset: %+v", got[0])
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
