package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const fakeCodexScript = `#!/bin/sh
here=$(dirname "$0")
printf '%s|CODEX_HOME=%s\n' "$*" "$CODEX_HOME" >> "$here/calls"
case "$1" in
login)
	if [ -f "$here/login.sleep" ]; then exec sleep 30; fi
	exit "$(cat "$here/login.exit")"
	;;
exec)
	cat > "$here/prompt"
	cat "$here/exec.jsonl"
	exit "$(cat "$here/exec.exit")"
	;;
app-server)
	while IFS= read -r line; do
		case "$line" in
		*'"method":"initialize"'*) printf '%s\n' '{"id":0,"result":{}}' ;;
		*'"method":"account/rateLimits/read"'*) cat "$here/ratelimits.json" ;;
		esac
	done
	;;
esac
`

type fakeCodex struct {
	dir string
}

func installFakeCodex(tb testing.TB) fakeCodex {
	tb.Helper()
	fake := fakeCodex{dir: tb.TempDir()}
	if err := os.WriteFile(fake.path(), []byte(fakeCodexScript), 0o755); err != nil {
		tb.Fatal(err)
	}
	fake.write(tb, "login.exit", "0")
	fake.write(tb, "exec.exit", "0")
	fake.write(tb, "exec.jsonl", "")
	fake.write(tb, "ratelimits.json", `{"id":1,"result":{"rateLimits":{}}}`)
	return fake
}

func (f fakeCodex) path() string {
	return filepath.Join(f.dir, "codex")
}

func (f fakeCodex) write(tb testing.TB, name, content string) {
	tb.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content+"\n"), 0o600); err != nil {
		tb.Fatal(err)
	}
}

func (f fakeCodex) read(tb testing.TB, name string) string {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil && !os.IsNotExist(err) {
		tb.Fatal(err)
	}
	return string(data)
}

func (f fakeCodex) access(codexHome string) contracts.ProbeAccess {
	return contracts.ProbeAccess{
		Getenv: func(name string) string {
			if name == contracts.EnvCodexHome {
				return codexHome
			}
			return ""
		},
		Config: contracts.PluginConfig{Settings: map[string]string{"cmd": f.path()}},
	}
}

func reason(notice contracts.Notice) string {
	for _, arg := range notice.Args {
		if arg.Name() == "Reason" || arg.Name() == "Command" {
			if text, isText := arg.Value().(string); isText {
				return text
			}
		}
	}
	return ""
}

func TestProbeLoggedInIsConnectedUnderTheProfileHome(t *testing.T) {
	t.Setenv(contracts.EnvCodexHome, "/host/codex")
	fake := installFakeCodex(t)
	report := probe(t.Context(), fake.access("/profiles/work"))
	if report.State != contracts.HealthOK || report.Detail.Code != contracts.CodeHealthConnected || report.Latency <= 0 {
		t.Fatalf("report = %+v, want ok and connected with a latency", report)
	}
	if calls := fake.read(t, "calls"); calls != "login status|CODEX_HOME=/profiles/work\n" {
		t.Fatalf("calls = %q, want one login status under the profile home", calls)
	}
}

func TestProbeNativeAccountDropsAHostCodexHomeTheAccessDoesNotName(t *testing.T) {
	t.Setenv(contracts.EnvCodexHome, "/host/codex")
	fake := installFakeCodex(t)
	probe(t.Context(), fake.access(""))
	if calls := fake.read(t, "calls"); calls != "login status|CODEX_HOME=\n" {
		t.Fatalf("calls = %q, want CODEX_HOME unset", calls)
	}
}

func TestProbeNotLoggedInIsOffWithALoginRemedy(t *testing.T) {
	tests := []struct {
		name    string
		account contracts.ProfileName
		command string
	}{
		{name: "native", command: "codex login"},
		{name: "profile", account: "work", command: "herrscher account add codex work"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := installFakeCodex(t)
			fake.write(t, "login.exit", "1")
			access := fake.access("")
			access.Account = test.account
			report := probe(t.Context(), access)
			if report.State != contracts.HealthOff || report.Detail.Code != contracts.CodeHealthNotLoggedIn {
				t.Fatalf("report = %+v, want off and not logged in", report)
			}
			if report.Remedy.Code != contracts.CodeHealthRemedyRun || reason(report.Remedy) != test.command {
				t.Errorf("remedy = %+v, want run %q", report.Remedy, test.command)
			}
		})
	}
}

func TestProbeMissingBinaryIsOff(t *testing.T) {
	access := installFakeCodex(t).access("")
	access.Config.Settings["cmd"] = filepath.Join(t.TempDir(), "absent")
	report := probe(t.Context(), access)
	if report.State != contracts.HealthOff || report.Detail.Code != contracts.CodeHealthUnreachable || reason(report.Detail) != reasonNotFound {
		t.Fatalf("report = %+v, want off and codex not found", report)
	}
}

func TestProbeStopsAtTheDeadline(t *testing.T) {
	fake := installFakeCodex(t)
	fake.write(t, "login.sleep", "")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	report := probe(ctx, fake.access(""))
	if report.State != contracts.HealthDown || reason(report.Detail) != reasonTimeout {
		t.Fatalf("report = %+v, want down on timeout", report)
	}
}

func TestProbeWithoutDeepRunsNoTurn(t *testing.T) {
	fake := installFakeCodex(t)
	probe(t.Context(), fake.access(""))
	if calls := fake.read(t, "calls"); strings.Contains(calls, "exec") {
		t.Fatalf("calls = %q, want no exec", calls)
	}
}

func TestProbeDeepRunsOneReadOnlyTurnOnTheConfiguredModel(t *testing.T) {
	fake := installFakeCodex(t)
	fake.write(t, "exec.jsonl", `{"type":"thread.started"}`+"\n"+`{"type":"turn.completed","usage":{"input_tokens":3}}`)
	access := fake.access("/profiles/work")
	access.Config.Settings["model"] = "gpt-mini"
	access.Deep = true
	report := probe(t.Context(), access)
	if report.State != contracts.HealthOK || report.Detail.Code != contracts.CodeHealthConnected {
		t.Fatalf("report = %+v, want ok", report)
	}
	calls := fake.read(t, "calls")
	execCall := "exec --json --skip-git-repo-check --ephemeral --sandbox read-only -c approval_policy=\"never\" --model gpt-mini -|CODEX_HOME=/profiles/work"
	if !strings.Contains(calls, execCall) {
		t.Fatalf("calls = %q, want %q", calls, execCall)
	}
	if prompt := fake.read(t, "prompt"); prompt != probePrompt {
		t.Errorf("prompt = %q, want %q", prompt, probePrompt)
	}
}

func TestProbeDeepFailedTurnIsDegradedWhenALimitIsSpent(t *testing.T) {
	fake := installFakeCodex(t)
	fake.write(t, "exec.jsonl", `{"type":"turn.failed","error":{"message":"usage limit"}}`)
	fake.write(t, "exec.exit", "1")
	fake.write(t, "ratelimits.json", `{"id":1,"result":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":300}}}}`)
	access := fake.access("")
	access.Deep = true
	report := probe(t.Context(), access)
	if report.State != contracts.HealthDegraded || report.Detail.Code != contracts.CodeHealthRateLimited {
		t.Fatalf("report = %+v, want degraded and rate limited", report)
	}
}

func TestProbeDeepFailedTurnIsDownWhenLimitsAreLeft(t *testing.T) {
	fake := installFakeCodex(t)
	fake.write(t, "exec.jsonl", `{"type":"turn.failed","error":{"message":"model not supported"}}`)
	fake.write(t, "exec.exit", "1")
	fake.write(t, "ratelimits.json", `{"id":1,"result":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":40,"windowDurationMins":300}}}}`)
	access := fake.access("")
	access.Deep = true
	report := probe(t.Context(), access)
	if report.State != contracts.HealthDown || reason(report.Detail) != reasonTurnFailed {
		t.Fatalf("report = %+v, want down and turn failed", report)
	}
}

func TestProbeDeepWithoutATurnEventIsDown(t *testing.T) {
	fake := installFakeCodex(t)
	fake.write(t, "exec.jsonl", "not json")
	fake.write(t, "exec.exit", "2")
	access := fake.access("")
	access.Deep = true
	report := probe(t.Context(), access)
	if report.State != contracts.HealthDown || reason(report.Detail) != reasonNoTurn {
		t.Fatalf("report = %+v, want down and no turn completed", report)
	}
}

func TestPluginRegistersProbeAndLimits(t *testing.T) {
	plugin := backendPlugin(t)
	if plugin.Probe == nil || plugin.Limits == nil {
		t.Fatal("the codex plugin registers no Probe or no Limits")
	}
}
