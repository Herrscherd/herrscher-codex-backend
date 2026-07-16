package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Herrscherd/herrscher-contracts"
)

// Config configures a Codex backend.
type Config struct {
	Kind    string
	Stream  bool
	Cmd     string
	Model   string
	Effort  string
	Dir     string
	Verbose bool
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
	kind := resolveBackend(c.Kind, c.Stream)
	switch kind {
	case "oneshot":
		if strings.TrimSpace(c.Cmd) == "" {
			return nil, fmt.Errorf("oneshot backend requires a non-empty Cmd")
		}
		cmdStr := c.Cmd
		return &oneShotResponder{run: func(ctx context.Context, p contracts.Prompt) (string, error) {
			return runCmd(ctx, cmdStr, c.Model, c.Effort, c.Dir, c.Verbose, p)
		}}, nil
	case "stream":
		return &streamResponder{ctx: ctx, base: streamBase(strings.Fields(c.Cmd)), model: c.Model, effort: c.Effort, dir: c.Dir, verbose: c.Verbose}, nil
	default:
		return nil, fmt.Errorf("unknown backend kind %q", kind)
	}
}

func runCmd(ctx context.Context, cmdStr, model, effort, dir string, verbose bool, p contracts.Prompt) (string, error) {
	fields := strings.Fields(cmdStr)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty Codex command")
	}
	content := withContext(p.Context, withAttachments(p.Content, p.Attachments))
	args := append([]string{}, fields[1:]...)
	args = append(args, "exec", "--json")
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+effort)
	}
	args = append(args, content)
	cmd := exec.CommandContext(ctx, fields[0], args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(content)
	cmd.Env = append(os.Environ(),
		"DCTL_MSG="+p.Content,
		"DCTL_AUTHOR="+p.Author,
		"DCTL_MESSAGE_ID="+p.MessageID,
		"DCTL_CHANNEL="+p.ChannelID,
		"DCTL_ATTACHMENTS="+strings.Join(p.Attachments, string(os.PathListSeparator)),
	)
	var stderr bytes.Buffer
	if verbose {
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stderr = &stderr
	}
	out, err := cmd.Output()
	if err != nil && stderr.Len() > 0 {
		return parseExecOutput(string(out)), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseExecOutput(string(out)), err
}

func parseExecOutput(out string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
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
			return lines[i]
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
