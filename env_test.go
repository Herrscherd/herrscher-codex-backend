package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Herrscherd/herrscher-contracts"
)

// TestNativeSpawnInheritsAndInjectionReplaces exercises the two spawn-time
// environment properties through a real child process, not through
// contracts.MergeEnv directly: on the native route the child sees the daemon's
// own environment, and on the gateway route an injected key REPLACES the
// inherited entry. The child sees one entry with the injected value: dropping
// the merge, or letting the inherited value win, would run the session on the
// machine's own login while the system believes it injected a gateway
// credential. Asserting through a real child rather than through
// contracts.MergeEnv is the point — that is the code path a spawn actually
// takes, and os/exec's own dedup (last entry wins) is part of it.
func TestNativeSpawnInheritsAndInjectionReplaces(t *testing.T) {
	const probeVar = "HERRSCHER_ENV_PROBE"
	// The script reports the value the child actually resolves AND how many
	// entries for it the child's environment carries, on one line (runCmd's
	// parseExecOutput keeps only the last non-empty output line).
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

// writeEnvProbeScript stands in for the codex CLI (same technique as
// TestRunCmdErrorIncludesStderr in backend_test.go). runCmd's execArgs
// unconditionally appends codex's own "exec --json -c ... -" flags after
// fields[0], so a plain "env" invocation would receive "exec" as its first
// non-assignment argument and try (and fail) to execute a program named
// "exec": a fake binary that ignores its argv sidesteps that.
//
// It cannot simply dump the full environment either: runCmd's
// parseExecOutput keeps only the LAST non-empty output line (it is built to
// extract the final agentMessage from real `codex exec --json` output), and
// once a full `env` dump is piped through any /bin/sh indirection, dash adds
// its own trailing PWD/SHLVL/_ variables — the assertion would then depend on
// shell internals rather than on the injected value. Printing exactly the two
// variables the tests care about, on a single explicit line, sidesteps both
// problems and survives parseExecOutput's truncation by construction.
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
	// The oneshot spawn must see Config.Env: we verify the value travels from
	// backend construction to the spawn closure's cmd.Env.
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
	// codex's oneshot spawn already sets its own DCTL_* variables. Injection
	// must not clobber them: contracts.MergeEnv only overwrites colliding keys,
	// and HERRSCHER_ENV_PROBE never collides with DCTL_MSG.
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

// waitForProbeFile polls for path to appear (a bounded deadline rather than a
// fixed sleep, since the child writes it asynchronously right after spawn) and
// returns its contents. It fails the test with a clear message if the file
// never shows up within the deadline.
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

// TestStreamSpawnAppliesInjectedEnv covers the app-server spawn site
// (startAppSession in stream.go), which has no vendor binary to run in CI. If
// the cmd.Env line there were ever dropped, a stream session would silently
// run against the user's own codex login while the system believed it was
// running on the paid gateway — a state the project's legal constraints
// forbid — and argument threading alone cannot catch that.
//
// startAppSession execs argv[0] directly (no shell), with argv built as base
// followed by appServerArgv's own flags. Passing base as ["sh", "-c", script]
// makes sh read the script from -c and treat the appended app-server flags as
// positional parameters ($0, $1, ...), which the script below never
// references. Each script records what it sees of the probe variable to a
// file and then execs `cat`, so the child blocks on stdin exactly like a real
// codex app-server process would — startAppSession returns a live session,
// not one that has already exited.
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
			defer func() { _ = sess.Close() }()
		}
		// The handshake (initialize/thread-start RPCs) fails against this fake
		// script, which is expected: we only care whether the probe var reached
		// the child before that failure, which the file proves independently of
		// startAppSession's return value.
		got := waitForProbeFile(t, probeFile)
		if got != "injected-stream-value" {
			t.Fatalf("probe file contains %q, want %q", got, "injected-stream-value")
		}
	})

	t.Run("nil env leaves the probe var absent", func(t *testing.T) {
		// Without this case, a passing "injected" test above cannot distinguish
		// real injection from an ambient variable that was already set.
		dir := t.TempDir()
		probeFile := filepath.Join(dir, "probe.txt")
		script := fmt.Sprintf(`if [ -z "${%s+x}" ]; then printf ABSENT > %q; else printf 'SET:%%s' "$%s" > %q; fi; exec cat`,
			probeVar, probeFile, probeVar, probeFile)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sess, err := startAppSession(ctx, ctx, []string{"sh", "-c", script}, "", "", dir, false, "", nil, nil)
		if err == nil {
			defer func() { _ = sess.Close() }()
		}
		got := waitForProbeFile(t, probeFile)
		if got != "ABSENT" {
			t.Fatalf("probe variable leaked into the child environment with nil injection: %q", got)
		}
	})
}
