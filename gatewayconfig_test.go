package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

func TestGatewayConfigDeclaresResponsesWireAPI(t *testing.T) {
	// codex 0.146.0 hard-rejects wire_api = "chat" at load time ("is no longer
	// supported... set wire_api = \"responses\""), verified live against the
	// installed CLI. A gateway route must therefore declare "responses", the
	// value the CLI actually accepts.
	got := gatewayConfigTOML("https://gw.example/openai")
	if !strings.Contains(got, `wire_api = "responses"`) {
		t.Fatalf("generated config lacks wire_api = \"responses\":\n%s", got)
	}
	if strings.Contains(got, `wire_api = "chat"`) {
		t.Fatalf("generated config declares the rejected wire_api = \"chat\":\n%s", got)
	}
	if !strings.Contains(got, `base_url = "https://gw.example/openai"`) {
		t.Fatalf("generated config lacks the base URL:\n%s", got)
	}
}

func TestGatewayConfigNeverEmbedsTheToken(t *testing.T) {
	// The token travels via env_key, never through the file: a CODEX_HOME left
	// on disk containing the token would survive the process and leak into a
	// bug report.
	got := gatewayConfigTOML("https://gw.example/openai")
	if !strings.Contains(got, `env_key = "NEUBLOX_TOKEN"`) {
		t.Fatalf("config does not reference the token by env_key:\n%s", got)
	}
	if strings.Contains(strings.ToLower(got), "token =") {
		t.Fatalf("config embeds a literal token:\n%s", got)
	}
}

func TestWriteGatewayHomeCreatesReadableConfig(t *testing.T) {
	dir := t.TempDir()
	home, err := writeGatewayHome(dir, "https://gw.example/openai")
	if err != nil {
		t.Fatalf("writeGatewayHome: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatalf("config.toml unreadable: %v", err)
	}
	if !strings.Contains(string(b), "model_providers.neublox") {
		t.Fatalf("config.toml content unexpected:\n%s", b)
	}
}

func TestWriteGatewayHomeIsPrivate(t *testing.T) {
	dir := t.TempDir()
	home, err := writeGatewayHome(dir, "https://gw.example/openai")
	if err != nil {
		t.Fatalf("writeGatewayHome: %v", err)
	}
	fi, err := os.Stat(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config.toml mode = %o, want 600", perm)
	}
}

// TestWriteGatewayHomeIsUniquePerCall proves two gateway spawns never share a
// CODEX_HOME. Before the fix, writeGatewayHome always resolved to the same
// filepath.Join(dir, "codex-home") regardless of baseURL or call: two
// concurrent sessions would race on one config.toml, and one session's
// traffic could end up flowing through another session's base_url/token
// pairing. Both directories must exist independently, each with its own
// config content.
func TestWriteGatewayHomeIsUniquePerCall(t *testing.T) {
	dir := t.TempDir()
	homeA, err := writeGatewayHome(dir, "https://gw-a.example/openai")
	if err != nil {
		t.Fatalf("writeGatewayHome A: %v", err)
	}
	homeB, err := writeGatewayHome(dir, "https://gw-b.example/openai")
	if err != nil {
		t.Fatalf("writeGatewayHome B: %v", err)
	}
	if homeA == homeB {
		t.Fatalf("two calls returned the same CODEX_HOME: %s", homeA)
	}

	cfgA, err := os.ReadFile(filepath.Join(homeA, "config.toml"))
	if err != nil {
		t.Fatalf("config.toml A unreadable: %v", err)
	}
	cfgB, err := os.ReadFile(filepath.Join(homeB, "config.toml"))
	if err != nil {
		t.Fatalf("config.toml B unreadable: %v", err)
	}
	if !strings.Contains(string(cfgA), "https://gw-a.example/openai") {
		t.Fatalf("home A config does not carry its own base_url:\n%s", cfgA)
	}
	if !strings.Contains(string(cfgB), "https://gw-b.example/openai") {
		t.Fatalf("home B config does not carry its own base_url:\n%s", cfgB)
	}
	if strings.Contains(string(cfgA), "gw-b.example") || strings.Contains(string(cfgB), "gw-a.example") {
		t.Fatalf("configs bled into each other:\nA:\n%s\nB:\n%s", cfgA, cfgB)
	}
}

// TestWriteGatewayHomeDoesNotAdoptExistingDirectory proves a pre-existing
// directory at the target path is never silently reused. Before the fix,
// os.MkdirAll on an already-existing directory is a no-op that does not
// re-apply the mode, so a pre-created (or attacker-controlled/symlinked)
// "codex-home" with looser permissions would be adopted rather than
// rejected. os.MkdirTemp always creates a fresh, unpredictable directory, so
// pre-seeding the guessable legacy path must have no effect on the result.
func TestWriteGatewayHomeDoesNotAdoptExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "codex-home")
	if err := os.MkdirAll(legacy, 0o777); err != nil {
		t.Fatalf("seeding legacy dir: %v", err)
	}
	poison := filepath.Join(legacy, "config.toml")
	if err := os.WriteFile(poison, []byte("poisoned"), 0o666); err != nil {
		t.Fatalf("seeding legacy config: %v", err)
	}

	home, err := writeGatewayHome(dir, "https://gw.example/openai")
	if err != nil {
		t.Fatalf("writeGatewayHome: %v", err)
	}
	if home == legacy {
		t.Fatalf("writeGatewayHome adopted the pre-existing legacy directory: %s", home)
	}
	fi, err := os.Stat(home)
	if err != nil {
		t.Fatalf("stat generated home: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("generated home mode = %o, want 700 (not adopted from a looser pre-existing dir)", perm)
	}
	b, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatalf("generated config.toml unreadable: %v", err)
	}
	if strings.Contains(string(b), "poisoned") {
		t.Fatalf("generated config carries content from the pre-existing legacy directory")
	}
}

// TestGatewayRouteSetsCodexHome proves the oneshot spawn actually sees
// CODEX_HOME pointing at a generated config once OPENAI_BASE_URL is present in
// Config.Env — the wiring in NewBackend, not just the generator in isolation.
func TestGatewayRouteSetsCodexHome(t *testing.T) {
	script := "#!/bin/sh\nprintf 'CODEX_HOME=%s\\n' \"$CODEX_HOME\"\n"
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	b, err := NewBackend(context.Background(), Config{
		Kind: "oneshot",
		Cmd:  path,
		Env: map[string]string{
			"OPENAI_BASE_URL": "https://gw.example/openai",
			"NEUBLOX_TOKEN":   "probe-token",
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	out, err := b.Respond(context.Background(), contracts.Prompt{Content: "x"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v (output %q)", err, out)
	}
	if !strings.Contains(out, "CODEX_HOME=") || strings.Contains(out, "CODEX_HOME=\n") {
		t.Fatalf("CODEX_HOME missing from the spawned environment:\n%s", out)
	}
	idx := strings.Index(out, "CODEX_HOME=")
	home := strings.TrimSpace(out[idx+len("CODEX_HOME="):])
	if home == "" {
		t.Fatalf("CODEX_HOME is empty:\n%s", out)
	}
	cfg, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatalf("generated config.toml unreadable at %s: %v", home, err)
	}
	if strings.Contains(string(cfg), "probe-token") {
		t.Fatalf("token leaked onto disk in generated config:\n%s", cfg)
	}
}

// TestOneShotResponderCloseRemovesGatewayHome proves the one-shot route does
// not leak a CODEX_HOME directory forever. Before the fix, nothing removed
// the generated directory anywhere; the fixed legacy path masked the leak by
// overwriting in place, but a per-spawn directory with no cleanup would
// accumulate one leftover directory per session on a long-running host.
func TestOneShotResponderCloseRemovesGatewayHome(t *testing.T) {
	script := "#!/bin/sh\nprintf 'CODEX_HOME=%s\\n' \"$CODEX_HOME\"\n"
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	b, err := NewBackend(context.Background(), Config{
		Kind: "oneshot",
		Cmd:  path,
		Env: map[string]string{
			"OPENAI_BASE_URL": "https://gw.example/openai",
			"NEUBLOX_TOKEN":   "probe-token",
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	out, err := b.Respond(context.Background(), contracts.Prompt{Content: "x"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v (output %q)", err, out)
	}
	idx := strings.Index(out, "CODEX_HOME=")
	if idx < 0 {
		t.Fatalf("CODEX_HOME missing from the spawned environment:\n%s", out)
	}
	home := strings.TrimSpace(out[idx+len("CODEX_HOME="):])
	if home == "" {
		t.Fatalf("CODEX_HOME is empty:\n%s", out)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("CODEX_HOME does not exist before Close: %v", err)
	}

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("CODEX_HOME still present after Close (session end): stat err = %v", err)
	}
}

// TestStreamResponderCloseRemovesGatewayHome is the streaming-route
// counterpart of TestOneShotResponderCloseRemovesGatewayHome: it exercises
// streamResponder.Close directly (with no app-server session ever started,
// the common case for a gateway backend that is torn down before its first
// turn) to prove the generated CODEX_HOME is removed regardless of whether an
// appSession exists.
func TestStreamResponderCloseRemovesGatewayHome(t *testing.T) {
	dir := t.TempDir()
	home, err := writeGatewayHome(dir, "https://gw.example/openai")
	if err != nil {
		t.Fatalf("writeGatewayHome: %v", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("CODEX_HOME does not exist before Close: %v", err)
	}

	r := &streamResponder{gatewayHome: home}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("CODEX_HOME still present after Close: stat err = %v", err)
	}
}

// TestNativeRouteLeavesCodexHomeUnset is the non-regression the brief calls
// out by name: with no gateway environment injected, CODEX_HOME must not be
// set at all, and the spawn must behave exactly as it does today. Deleting
// the CODEX_HOME assignment in NewBackend must turn this test red on its own
// — it does not merely share a fixture with the gateway-route test above.
func TestNativeRouteLeavesCodexHomeUnset(t *testing.T) {
	script := "#!/bin/sh\nif [ -z \"${CODEX_HOME+x}\" ]; then printf 'ABSENT'; else printf 'SET:%s' \"$CODEX_HOME\"; fi\n"
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	b, err := NewBackend(context.Background(), Config{
		Kind: "oneshot",
		Cmd:  path,
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	out, err := b.Respond(context.Background(), contracts.Prompt{Content: "x"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v (output %q)", err, out)
	}
	if out != "ABSENT" {
		t.Fatalf("CODEX_HOME leaked into the native route: %q", out)
	}
}

// TestFailingNewBackendLeavesNoGatewayHome pins the cleanup on NewBackend's
// error paths. The generated CODEX_HOME is owned by the responder that gets
// returned (which removes it on Close); when construction fails no responder
// exists, so nothing would ever remove it. The host retries a misconfigured
// gateway session, so before the fix each attempt left one /tmp/codex-home-*
// behind for the daemon's lifetime.
//
// TMPDIR is redirected at a fresh directory because NewBackend materializes
// the home under os.TempDir(); counting entries there is what makes the leak
// observable at all.
func TestFailingNewBackendLeavesNoGatewayHome(t *testing.T) {
	gatewayEnv := map[string]string{
		contracts.EnvOpenAIBaseURL: "https://gw.example/openai",
		contracts.EnvNeubloxToken:  "probe-token",
	}
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"unknown kind", Config{Kind: "bogus-kind", Env: gatewayEnv}},
		{"oneshot with empty Cmd", Config{Kind: "oneshot", Cmd: "", Env: gatewayEnv}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)

			if _, err := NewBackend(context.Background(), tc.cfg); err == nil {
				t.Fatal("NewBackend succeeded, want an error")
			}
			left, err := os.ReadDir(tmp)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(left) != 0 {
				var names []string
				for _, e := range left {
					names = append(names, e.Name())
				}
				t.Fatalf("failed NewBackend leaked %d entries under TMPDIR: %v", len(left), names)
			}
		})
	}
}
