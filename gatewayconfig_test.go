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
