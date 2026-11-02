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

func discardGatewayHome(gatewayHome string, err error) error {
	if gatewayHome == "" {
		return err
	}
	return errors.Join(err, os.RemoveAll(gatewayHome))
}

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

// NewBackend builds a configured Codex backend. ctx must not be nil.
func NewBackend(ctx context.Context, cfg Config) (contracts.Backend, error) {
	if err := checkGatewayPair(cfg.Env); err != nil {
		return nil, err
	}
	var gatewayHome string
	if base := cfg.Env[contracts.EnvOpenAIBaseURL]; base != "" {
		home, err := writeGatewayHome(os.TempDir(), base)
		if err != nil {
			return nil, err
		}
		gatewayHome = home
		env := make(map[string]string, len(cfg.Env)+1)
		for k, v := range cfg.Env {
			env[k] = v
		}
		env["CODEX_HOME"] = home
		cfg.Env = env
	}
	fail := func(err error) (contracts.Backend, error) {
		return nil, discardGatewayHome(gatewayHome, err)
	}
	approvals := approverFromEnv()
	kind := resolveBackend(cfg.Kind, cfg.Stream)
	switch kind {
	case kindOneShot:
		warnOneShotUngated(approvals, os.Stderr)
		spec := commandOverrides(cfg.Cmd, cfg.Model, cfg.Effort)
		cmd := strings.Join(spec.argv, " ")
		if cmd == "" {
			return fail(errOneShotNoCmd)
		}
		return &oneShotResponder{
			run: func(ctx context.Context, p contracts.Prompt) (string, error) {
				return runCmd(ctx, cmd, spec.model, spec.effort, cfg.Dir, cfg.Verbose, cfg.Env, p)
			},
			gatewayHomeRemovedOnClose: gatewayHome,
		}, nil
	case kindStream:
		spec := commandOverrides(cfg.Cmd, cfg.Model, cfg.Effort)
		cfg.Model, cfg.Effort = spec.model, spec.effort
		if runtime.GOOS == "windows" && os.Getenv("HERRSCHER_CODEX_ONESHOT_WINDOWS") == "1" {
			warnOneShotUngated(approvals, os.Stderr)
			cmd := strings.Join(streamBase(spec.argv), " ")
			return &oneShotResponder{
				run: func(ctx context.Context, p contracts.Prompt) (string, error) {
					return runCmd(ctx, cmd, cfg.Model, cfg.Effort, cfg.Dir, cfg.Verbose, cfg.Env, p)
				},
				gatewayHomeRemovedOnClose: gatewayHome,
			}, nil
		}
		responder := &streamResponder{base: streamBase(spec.argv), model: cfg.Model, effort: cfg.Effort, dir: cfg.Dir, verbose: cfg.Verbose, resumeID: cfg.ResumeID, env: cfg.Env, gatewayHomeRemovedOnClose: gatewayHome, approvalsIfGated: approvals}
		responder.start = responder.launchedWithin(ctx)
		return responder, nil
	default:
		return fail(fmt.Errorf("%w %q", errUnknownKind, kind))
	}
}

type commandSpec struct {
	argv   []string
	model  string
	effort string
}

func commandOverrides(cmd, model, effort string) commandSpec {
	spec := streamCommand(strings.Fields(cmd))
	if spec.model == "" {
		spec.model = model
	}
	if spec.effort == "" {
		spec.effort = effort
	}
	return spec
}

func streamCommand(fields []string) commandSpec {
	var spec commandSpec
	for index := 0; index < len(fields); index++ {
		switch fields[index] {
		case "--model", "-m":
			if index+1 < len(fields) {
				spec.model = fields[index+1]
				index++
				continue
			}
		case "--effort":
			if index+1 < len(fields) {
				spec.effort = fields[index+1]
				index++
				continue
			}
		}
		spec.argv = append(spec.argv, fields[index])
	}
	return spec
}

const (
	stderrCaptureLimit = 8 << 10
	stdinPromptArg     = "-"
)

type boundedBuffer struct {
	limit   int
	buf     []byte
	dropped int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		if len(data) <= room {
			b.buf = append(b.buf, data...)
			return len(data), nil
		}
		b.buf = append(b.buf, data[:room]...)
		b.dropped += len(data) - room
		return len(data), nil
	}
	b.dropped += len(data)
	return len(data), nil
}

func (b *boundedBuffer) String() string {
	if b.dropped > 0 {
		return string(b.buf) + fmt.Sprintf("… (%d more bytes truncated)", b.dropped)
	}
	return string(b.buf)
}

func runCmd(ctx context.Context, cmdStr, model, effort, dir string, verbose bool, env map[string]string, prompt contracts.Prompt) (string, error) {
	fields := strings.Fields(cmdStr)
	if len(fields) == 0 {
		return "", errEmptyCommand
	}
	content := withContext(prompt.Context, withAttachments(prompt.Content, prompt.Attachments))
	args := append(execArgs(fields, model, effort), stdinPromptArg)
	cmd := exec.CommandContext(ctx, fields[0], args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(content)
	cmd.Env = contracts.MergeEnv(append(os.Environ(),
		"DCTL_MSG="+prompt.Content,
		"DCTL_AUTHOR="+prompt.Author,
		"DCTL_MESSAGE_ID="+prompt.MessageID,
		"DCTL_CHANNEL="+prompt.ChannelID,
		"DCTL_ATTACHMENTS="+strings.Join(prompt.Attachments, string(os.PathListSeparator)),
	), env)
	errBuf := &boundedBuffer{limit: stderrCaptureLimit}
	if verbose {
		cmd.Stderr = io.MultiWriter(os.Stderr, errBuf)
	} else {
		cmd.Stderr = errBuf
	}
	out, err := cmd.Output()
	if err != nil {
		return parseExecOutput(string(out)), &execError{cause: err, stderr: strings.TrimSpace(errBuf.String())}
	}
	return parseExecOutput(string(out)), nil
}

type execError struct {
	cause  error
	stderr string
}

func (e *execError) Error() string {
	if e.stderr == "" {
		return fmt.Sprintf("codex exec failed: %v", e.cause)
	}
	return fmt.Sprintf("codex exec failed: %v: %s", e.cause, e.stderr)
}

func (e *execError) Unwrap() error {
	return e.cause
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

func parseExecOutput(out string) string {
	fallback := ""
	for out != "" {
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
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && (event.Item.Type == "agentMessage" || event.Item.Type == "agent_message") && event.Item.Text != "" {
			return event.Item.Text
		}
		if !strings.HasPrefix(line, "{") && fallback == "" {
			fallback = line
		}
	}
	return fallback
}
