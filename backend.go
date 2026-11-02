package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
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
	ResumeID string

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
		return kindStream
	}
	return kindOneShot
}

var errGatewayPairRefused = errors.New("refusing to spawn")

var (
	errOneShotNoCmd = errors.New("oneshot backend requires a non-empty Cmd")
	errEmptyCommand = errors.New("empty Codex command")
	errUnknownKind  = errors.New("unknown backend kind")
)

func checkGatewayPair(env map[string]string) error {
	if env[contracts.EnvOpenAIBaseURL] == "" {
		return nil
	}
	if strings.TrimSpace(env[contracts.EnvNeubloxToken]) == "" {
		return fmt.Errorf("%w: %s is set without %s; the session would run on the machine's own subscription while being routed through the gateway",
			errGatewayPairRefused, contracts.EnvOpenAIBaseURL, contracts.EnvNeubloxToken)
	}
	return nil
}

// NewBackend builds a configured Codex backend.
func NewBackend(ctx context.Context, c Config) (contracts.Backend, error) {
	if err := checkGatewayPair(c.Env); err != nil {
		return nil, err
	}
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
	fail := func(err error) (contracts.Backend, error) {
		if gatewayHome != "" {
			return nil, errors.Join(err, os.RemoveAll(gatewayHome))
		}
		return nil, err
	}
	ap := approverFromEnv()
	kind := resolveBackend(c.Kind, c.Stream)
	switch kind {
	case kindOneShot:
		warnOneShotUngated(ap, os.Stderr)
		cmd, model, effort := oneShotCommand(c.Cmd, c.Model, c.Effort)
		if cmd == "" {
			return fail(errOneShotNoCmd)
		}
		return &oneShotResponder{
			run: func(ctx context.Context, p contracts.Prompt) (string, error) {
				return runCmd(ctx, cmd, model, effort, c.Dir, c.Verbose, c.Env, p)
			},
			gatewayHome: gatewayHome,
		}, nil
	case kindStream:
		base, model, effort := commandOverrides(c.Cmd, c.Model, c.Effort)
		c.Model, c.Effort = model, effort
		if runtime.GOOS == "windows" && os.Getenv("HERRSCHER_CODEX_ONESHOT_WINDOWS") == "1" {
			warnOneShotUngated(ap, os.Stderr)
			cmd := strings.Join(streamBase(base), " ")
			return &oneShotResponder{
				run: func(ctx context.Context, p contracts.Prompt) (string, error) {
					return runCmd(ctx, cmd, c.Model, c.Effort, c.Dir, c.Verbose, c.Env, p)
				},
				gatewayHome: gatewayHome,
			}, nil
		}
		return &streamResponder{ctx: ctx, base: streamBase(base), model: c.Model, effort: c.Effort, dir: c.Dir, verbose: c.Verbose, resumeID: c.ResumeID, env: c.Env, gatewayHome: gatewayHome, ap: ap}, nil
	default:
		return fail(fmt.Errorf("%w %q", errUnknownKind, kind))
	}
}

func commandOverrides(cmd, model, effort string) ([]string, string, string) {
	base, commandModel, commandEffort := streamCommand(strings.Fields(cmd))
	if commandModel != "" {
		model = commandModel
	}
	if commandEffort != "" {
		effort = commandEffort
	}
	return base, model, effort
}

func oneShotCommand(cmd, model, effort string) (string, string, string) {
	base, model, effort := commandOverrides(cmd, model, effort)
	return strings.Join(base, " "), model, effort
}

func streamCommand(fields []string) ([]string, string, string) {
	var base []string
	var model, effort string
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

const stderrCaptureLimit = 8 << 10

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

func runCmd(ctx context.Context, cmdStr, model, effort, dir string, verbose bool, env map[string]string, p contracts.Prompt) (string, error) {
	fields := strings.Fields(cmdStr)
	if len(fields) == 0 {
		return "", errEmptyCommand
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

func execPrompt(content string) (string, string) {
	return "-", content
}

func parseExecOutput(out string) string {
	fallback := ""
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
		if !strings.HasPrefix(line, "{") && fallback == "" {
			fallback = line
		}
	}
	return fallback
}
