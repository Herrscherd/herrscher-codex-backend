package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestNativeSpawnInheritsAndInjectionReplaces(t *testing.T) {
	const probeVar = "HERRSCHER_ENV_PROBE"
	path := filepath.Join(t.TempDir(), "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'value=%%s count=%%s\\n' \"$%s\" \"$(env | grep -c '^%s=')\"\n", probeVar, probeVar)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, env map[string]string) string {
		t.Helper()
		b, err := NewBackend(context.Background(), Config{Kind: "oneshot", Cmd: path, Env: env})
		if err != nil {
			t.Fatalf("NewBackend: %v", err)
		}
		out, err := b.Respond(context.Background(), contracts.Prompt{Content: "x"}, nil)
		if err != nil {
			t.Fatalf("Respond: %v (output %q)", err, out)
		}
		return strings.TrimSpace(out)
	}

	t.Run("native spawn inherits the daemon environment", func(t *testing.T) {
		t.Setenv(probeVar, "inherited")
		if got := run(t, nil); got != "value=inherited count=1" {
			t.Fatalf("native child env = %q, want value=inherited count=1", got)
		}
	})

	t.Run("injection replaces rather than appends", func(t *testing.T) {
		t.Setenv(probeVar, "inherited")
		if got := run(t, map[string]string{probeVar: "injected"}); got != "value=injected count=1" {
			t.Fatalf("injected child env = %q, want value=injected count=1", got)
		}
	})
}

func writeEnvProbeScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf 'DCTL_MSG=%s HERRSCHER_ENV_PROBE=%s\\n' \"$DCTL_MSG\" \"$HERRSCHER_ENV_PROBE\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigCarriesEnvToOneShot(t *testing.T) {
	b, err := NewBackend(context.Background(), Config{
		Kind: "oneshot",
		Cmd:  writeEnvProbeScript(t),
		Env:  map[string]string{"HERRSCHER_ENV_PROBE": "injected"},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	out, err := b.Respond(context.Background(), contracts.Prompt{Content: "x"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v (output %q)", err, out)
	}
	if !strings.Contains(out, "HERRSCHER_ENV_PROBE=injected") {
		t.Fatalf("injected variable absent from the child environment:\n%s", out)
	}
}

func TestOneShotKeepsItsOwnVariables(t *testing.T) {
	b, err := NewBackend(context.Background(), Config{
		Kind: "oneshot",
		Cmd:  writeEnvProbeScript(t),
		Env:  map[string]string{"HERRSCHER_ENV_PROBE": "injected"},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	out, err := b.Respond(context.Background(), contracts.Prompt{Content: "hello", Author: "someone"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v (output %q)", err, out)
	}
	if !strings.Contains(out, "DCTL_MSG=hello") {
		t.Fatalf("injection clobbered the backend's own variables:\n%s", out)
	}
}

func waitForProbeFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			return string(b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("probe file %s never appeared within the deadline", path)
	return ""
}

func TestStreamSpawnAppliesInjectedEnv(t *testing.T) {
	const probeVar = "NEUBLOX_STREAM_PROBE"

	t.Run("injected value reaches the child", func(t *testing.T) {
		dir := t.TempDir()
		probeFile := filepath.Join(dir, "probe.txt")
		script := fmt.Sprintf(`printf '%%s' "$%s" > %q; exec cat`, probeVar, probeFile)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sess, err := startAppSession(ctx, ctx, []string{"sh", "-c", script}, "", "", dir, false, "", map[string]string{probeVar: "injected-stream-value"}, nil)
		if err == nil {
			defer closeChecked(t, sess)
		}
		got := waitForProbeFile(t, probeFile)
		if got != "injected-stream-value" {
			t.Fatalf("probe file contains %q, want %q", got, "injected-stream-value")
		}
	})

	t.Run("nil env leaves the probe var absent", func(t *testing.T) {
		dir := t.TempDir()
		probeFile := filepath.Join(dir, "probe.txt")
		script := fmt.Sprintf(`if [ -z "${%s+x}" ]; then printf ABSENT > %q; else printf 'SET:%%s' "$%s" > %q; fi; exec cat`,
			probeVar, probeFile, probeVar, probeFile)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sess, err := startAppSession(ctx, ctx, []string{"sh", "-c", script}, "", "", dir, false, "", nil, nil)
		if err == nil {
			defer closeChecked(t, sess)
		}
		got := waitForProbeFile(t, probeFile)
		if got != "ABSENT" {
			t.Fatalf("probe variable leaked into the child environment with nil injection: %q", got)
		}
	})
}
