package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Herrscherd/herrscher-contracts"
)

// Config configures a Codex backend.
type Config struct {
	Kind     string
	Stream   bool
	Cmd      string
	Model    string
	Effort   string
	Dir      string
	Verbose  bool
	ResumeID string // codex thread id to resume on first start ("" = fresh)

	// Env is injected into the child process's environment at every spawn. It
	// carries gateway credentials and is NEVER persisted or logged: that is
	// what distinguishes it from Cmd, which ends up in state.json and in `ps`.
	Env map[string]string
}

func resolveBackend(kind string, stream bool) string {
	if kind != "" {
		return kind
	}
	if stream {
		return "stream"
	}
	return "oneshot"
}

// NewBackend builds a configured Codex backend.
func NewBackend(ctx context.Context, c Config) (contracts.Backend, error) {
	// A gateway route is present when the host injected OPENAI_BASE_URL (Task
	// 8). Unlike the claude CLI, codex is not driven by environment variables
	// alone: it needs a custom provider declared in config.toml under
	// CODEX_HOME. Materialize a per-spawn disposable one and point CODEX_HOME
	// at it. The map is copied rather than mutated in place: c.Env comes from
	// the host and may be shared across multiple NewBackend calls. gatewayHome
	// is non-empty only on this route, and is the directory the returned
	// responder must remove when its session ends (see oneShotResponder and
	// streamResponder Close in stream.go).
	var gatewayHome string
	if base := c.Env[contracts.EnvOpenAIBaseURL]; base != "" {
		home, err := writeGatewayHome(os.TempDir(), base)
		if err != nil {
			return nil, err
		}
		gatewayHome = home
		env := make(map[string]string, len(c.Env)+1)
		for k, v := range c.Env {
			env[k] = v
		}
		env["CODEX_HOME"] = home
		c.Env = env
	}
	kind := resolveBackend(c.Kind, c.Stream)
	switch kind {
	case "oneshot":
		cmd, model, effort := oneShotCommand(c.Cmd, c.Model, c.Effort)
		if cmd == "" {
			return nil, fmt.Errorf("oneshot backend requires a non-empty Cmd")
		}
		return &oneShotResponder{
			run: func(ctx context.Context, p contracts.Prompt) (string, error) {
				return runCmd(ctx, cmd, model, effort, c.Dir, c.Verbose, c.Env, p)
			},
			gatewayHome: gatewayHome,
		}, nil
	case "stream":
		base, commandModel, commandEffort := streamCommand(strings.Fields(c.Cmd))
		if commandModel != "" {
			c.Model = commandModel
		}
		if commandEffort != "" {
			c.Effort = commandEffort
		}
		// Codex app-server 0.145 can remain alive without completing turns on
		// Windows. The normal exec transport is reliable there and preserves
		// the same model/effort semantics, so prefer it until app-server is stable.
		if runtime.GOOS == "windows" && os.Getenv("HERRSCHER_CODEX_ONESHOT_WINDOWS") == "1" {
			cmd := strings.Join(streamBase(base), " ")
			return &oneShotResponder{
				run: func(ctx context.Context, p contracts.Prompt) (string, error) {
					return runCmd(ctx, cmd, c.Model, c.Effort, c.Dir, c.Verbose, c.Env, p)
				},
				gatewayHome: gatewayHome,
			}, nil
		}
		return &streamResponder{ctx: ctx, base: streamBase(base), model: c.Model, effort: c.Effort, dir: c.Dir, verbose: c.Verbose, resumeID: c.ResumeID, env: c.Env, gatewayHome: gatewayHome}, nil
	default:
		return nil, fmt.Errorf("unknown backend kind %q", kind)
	}
}

func oneShotCommand(cmd, model, effort string) (string, string, string) {
	base, commandModel, commandEffort := streamCommand(strings.Fields(cmd))
	if commandModel != "" {
		model = commandModel
	}
	if commandEffort != "" {
		effort = commandEffort
	}
	return strings.Join(base, " "), model, effort
}

// streamCommand separates turn configuration from the executable argv. Model
// and effort belong to app-server RPC requests; leaving them before
// "app-server" makes the Codex CLI reject the command and close stdout.
func streamCommand(fields []string) (base []string, model, effort string) {
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "--model", "-m":
			if i+1 < len(fields) {
				model = fields[i+1]
				i++
				continue
			}
		case "--effort":
			if i+1 < len(fields) {
				effort = fields[i+1]
				i++
				continue
			}
		}
		base = append(base, fields[i])
	}
	return base, model, effort
}

// stderrCaptureLimit bounds how much child stderr is retained for error
// reporting. 8 KiB is far more than any codex diagnostic needs while keeping a
// runaway process from ballooning memory; the overflow is dropped and marked.
const stderrCaptureLimit = 8 << 10

// boundedBuffer accumulates at most limit bytes and reports the drop. Writes
// past the limit are discarded rather than failing, so the child never sees a
// short write / broken pipe just because its stderr got noisy.
type boundedBuffer struct {
	limit   int
	buf     []byte
	dropped int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		if len(p) <= room {
			b.buf = append(b.buf, p...)
			return len(p), nil
		}
		b.buf = append(b.buf, p[:room]...)
		b.dropped += len(p) - room
		return len(p), nil
	}
	b.dropped += len(p)
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	if b.dropped > 0 {
		return string(b.buf) + fmt.Sprintf("… (%d more bytes truncated)", b.dropped)
	}
	return string(b.buf)
}

// runCmd executes cmdStr (the codex CLI plus flags) as a one-shot exec. env is
// merged over the daemon's inherited environment plus this call's own DCTL_*
// variables (see contracts.MergeEnv): with no injection, the child process
// environment is unchanged.
func runCmd(ctx context.Context, cmdStr, model, effort, dir string, verbose bool, env map[string]string, p contracts.Prompt) (string, error) {
	fields := strings.Fields(cmdStr)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty Codex command")
	}
	content := withContext(p.Context, withAttachments(p.Content, p.Attachments))
	args := execArgs(fields, model, effort)
	promptArg, promptStdin := execPrompt(content)
	args = append(args, promptArg)
	cmd := exec.CommandContext(ctx, fields[0], args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(promptStdin)
	cmd.Env = contracts.MergeEnv(append(os.Environ(),
		"DCTL_MSG="+p.Content,
		"DCTL_AUTHOR="+p.Author,
		"DCTL_MESSAGE_ID="+p.MessageID,
		"DCTL_CHANNEL="+p.ChannelID,
		"DCTL_ATTACHMENTS="+strings.Join(p.Attachments, string(os.PathListSeparator)),
	), env)
	// Capture stderr so a failing codex CLI reports its own diagnostic instead of
	// a bare exit status. cmd.Output() only fills ExitError.Stderr when Stderr is
	// nil, and we need the verbose passthrough, so capture it explicitly.
	errBuf := &boundedBuffer{limit: stderrCaptureLimit}
	if verbose {
		cmd.Stderr = io.MultiWriter(os.Stderr, errBuf)
	} else {
		cmd.Stderr = errBuf
	}
	out, err := cmd.Output()
	if err != nil {
		if detail := strings.TrimSpace(errBuf.String()); detail != "" {
			return parseExecOutput(string(out)), fmt.Errorf("codex exec failed: %w: %s", err, detail)
		}
		return parseExecOutput(string(out)), fmt.Errorf("codex exec failed: %w", err)
	}
	return parseExecOutput(string(out)), nil
}

func execArgs(fields []string, model, effort string) []string {
	args := append([]string{}, fields[1:]...)
	args = append(args,
		"exec", "--json",
		// Headless: there is no human to answer approval prompts, so the agent
		// runs non-interactively. sandbox_mode bounds what that permits — file
		// and command execution stay confined to the worktree, and escalation is
		// refused rather than blindly run. The neublox MCP is the one surface we
		// explicitly trust to auto-approve.
		"-c", `approval_policy="never"`,
		"-c", `sandbox_mode="workspace-write"`,
		"-c", `mcp_servers.neublox.default_tools_approval_mode="approve"`,
	)
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+effort)
	}
	return args
}

func execPrompt(content string) (argument, stdin string) {
	return "-", content
}

func parseExecOutput(out string) string {
	for len(out) > 0 {
		idx := strings.LastIndexByte(out, '\n')
		raw := out[idx+1:]
		if idx < 0 {
			out = ""
		} else {
			out = out[:idx]
		}
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && (ev.Item.Type == "agentMessage" || ev.Item.Type == "agent_message") && ev.Item.Text != "" {
			return ev.Item.Text
		}
		if !strings.HasPrefix(line, "{") {
			return raw
		}
	}
	return ""
}

var modelPresets = []struct {
	label   string
	model   string
	efforts []string
}{
	{"GPT-5.6 Sol", "gpt-5.6-sol", []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
	{"GPT-5.6 Terra", "gpt-5.6-terra", []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
	{"GPT-5.6 Luna", "gpt-5.6-luna", []string{"low", "medium", "high", "xhigh", "max"}},
	{"GPT-5.5", "gpt-5.5", []string{"low", "medium", "high", "xhigh"}},
	{"GPT-5.4", "gpt-5.4", []string{"low", "medium", "high", "xhigh"}},
	{"GPT-5.4 Mini", "gpt-5.4-mini", []string{"low", "medium", "high", "xhigh"}},
	{"GPT-5.3 Codex Spark", "gpt-5.3-codex-spark", []string{"low", "medium", "high", "xhigh"}},
}

// CommandPresets returns model × reasoning-effort command suggestions.
func CommandPresets(bin string) []contracts.Choice {
	total := 0
	for _, m := range modelPresets {
		total += len(m.efforts)
	}
	out := make([]contracts.Choice, 0, total)
	for _, m := range modelPresets {
		for _, e := range m.efforts {
			out = append(out, contracts.Choice{
				Label: m.label + " · " + e,
				Value: bin + " --model " + m.model + " -c model_reasoning_effort=" + e,
			})
		}
	}
	return out
}
