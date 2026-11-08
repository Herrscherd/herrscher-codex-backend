package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	probeWaitDelay    = 2 * time.Second
	probeOutputLimit  = 64 << 10
	probePrompt       = "Reply with the single word OK."
	nativeLoginRemedy = "codex login"
	profileAddRemedy  = "herrscher account add codex "
	reasonTimeout     = "timeout"
	reasonCanceled    = "canceled"
	reasonNotFound    = "codex not found"
	reasonNoStart     = "codex did not start"
	reasonTurnFailed  = "turn failed"
	reasonNoTurn      = "no turn completed"
	fullyUsedPercent  = 100
)

type probeTarget struct {
	argv    []string
	model   string
	env     []string
	account contracts.ProfileName
}

func probeTargetOf(access contracts.ProbeAccess) probeTarget {
	getenv := access.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	spec := commandOverrides(settingOrEnv(access.Config, "cmd", getenv, "CODEX_CMD"), settingOrEnv(access.Config, "model", getenv, "CODEX_MODEL"), "")
	return probeTarget{argv: streamBase(spec.argv), model: spec.model, env: profileEnv(getenv), account: access.Account}
}

func settingOrEnv(config contracts.PluginConfig, key string, getenv func(string) string, name string) string {
	if value := config.Get(key); value != "" {
		return value
	}
	return getenv(name)
}

func profileEnv(getenv func(string) string) []string {
	homePrefix := contracts.EnvCodexHome + "="
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool { return strings.HasPrefix(entry, homePrefix) })
	if home := getenv(contracts.EnvCodexHome); home != "" {
		env = append(env, homePrefix+home)
	}
	return env
}

func (t probeTarget) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, t.argv[0], slices.Concat(t.argv[1:], args)...)
	configureChild(cmd)
	cmd.WaitDelay = probeWaitDelay
	cmd.Env = t.env
	return cmd
}

func probe(ctx context.Context, access contracts.ProbeAccess) contracts.ProbeReport {
	target := probeTargetOf(access)
	report := target.loginStatus(ctx)
	if report.State != contracts.HealthOK || !access.Deep {
		return report
	}
	return target.minimalTurn(ctx)
}

func (t probeTarget) loginStatus(ctx context.Context) contracts.ProbeReport {
	started := time.Now()
	err := t.command(ctx, "login", "status").Run()
	latency := time.Since(started)
	if err == nil {
		return contracts.ProbeReport{State: contracts.HealthOK, Latency: latency, Detail: contracts.Notice{Code: contracts.CodeHealthConnected}}
	}
	if exitedOnItsOwn(ctx, err) {
		return contracts.ProbeReport{
			State:   contracts.HealthOff,
			Latency: latency,
			Detail:  contracts.Notice{Code: contracts.CodeHealthNotLoggedIn},
			Remedy:  loginRemedy(t.account),
		}
	}
	return notRun(ctx, err)
}

func loginRemedy(account contracts.ProfileName) contracts.Notice {
	command := nativeLoginRemedy
	if account != "" {
		command = profileAddRemedy + string(account)
	}
	return contracts.Notice{Code: contracts.CodeHealthRemedyRun, Args: []contracts.Arg{contracts.Text("Command", command)}}
}

func notRun(ctx context.Context, err error) contracts.ProbeReport {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return unreachable(contracts.HealthDown, reasonTimeout)
	case ctx.Err() != nil:
		return unreachable(contracts.HealthDown, reasonCanceled)
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return unreachable(contracts.HealthOff, reasonNotFound)
	}
	return unreachable(contracts.HealthDown, reasonNoStart)
}

func unreachable(state contracts.HealthState, reason string) contracts.ProbeReport {
	return contracts.ProbeReport{State: state, Detail: contracts.Notice{Code: contracts.CodeHealthUnreachable, Args: []contracts.Arg{contracts.Text("Reason", reason)}}}
}

type probeTurn int

const (
	probeTurnMissing probeTurn = iota + 1
	probeTurnCompleted
	probeTurnFailed
)

func (t probeTarget) minimalTurn(ctx context.Context) contracts.ProbeReport {
	args := []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral", "--sandbox", "read-only", "-c", `approval_policy="never"`}
	if t.model != "" {
		args = append(args, "--model", t.model)
	}
	cmd := t.command(ctx, append(args, stdinPromptArg)...)
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(probePrompt)
	output := &boundedBuffer{limit: probeOutputLimit}
	cmd.Stdout = output
	started := time.Now()
	runErr := cmd.Run()
	latency := time.Since(started)
	switch probeTurnOf(string(output.buf)) {
	case probeTurnCompleted:
		return contracts.ProbeReport{State: contracts.HealthOK, Latency: latency, Detail: contracts.Notice{Code: contracts.CodeHealthConnected}}
	case probeTurnFailed:
		return t.failedTurn(ctx, latency)
	case probeTurnMissing:
		return missingTurn(ctx, runErr, latency)
	}
	return missingTurn(ctx, runErr, latency)
}

func missingTurn(ctx context.Context, runErr error, latency time.Duration) contracts.ProbeReport {
	if runErr != nil && !exitedOnItsOwn(ctx, runErr) {
		return notRun(ctx, runErr)
	}
	return withLatency(unreachable(contracts.HealthDown, reasonNoTurn), latency)
}

func exitedOnItsOwn(ctx context.Context, err error) bool {
	_, exited := errors.AsType[*exec.ExitError](err)
	return exited && ctx.Err() == nil
}

func withLatency(report contracts.ProbeReport, latency time.Duration) contracts.ProbeReport {
	report.Latency = latency
	return report
}

func probeTurnOf(output string) probeTurn {
	for line := range strings.Lines(output) {
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		switch event.Type {
		case "turn.completed":
			return probeTurnCompleted
		case "turn.failed":
			return probeTurnFailed
		}
	}
	return probeTurnMissing
}

func (t probeTarget) failedTurn(ctx context.Context, latency time.Duration) contracts.ProbeReport {
	limits, err := t.limits(ctx)
	if err == nil && slices.ContainsFunc(limits, func(limit contracts.Limit) bool { return limit.UsedPercent >= fullyUsedPercent }) {
		return contracts.ProbeReport{State: contracts.HealthDegraded, Latency: latency, Detail: contracts.Notice{Code: contracts.CodeHealthRateLimited}}
	}
	return withLatency(unreachable(contracts.HealthDown, reasonTurnFailed), latency)
}
