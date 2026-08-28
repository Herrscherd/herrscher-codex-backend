package codex

import (
	"context"

	"github.com/Herrscherd/herrscher-contracts"
)

func init() {
	contracts.Register(contracts.Plugin{
		Manifest: contracts.Manifest{
			Kind: "codex", Category: contracts.CategoryBackend,
			Status: contracts.StatusLive,
			Config: []contracts.Setting{
				{Key: "cmd", Env: "CODEX_CMD", Help: "base command to run the agent", Default: "codex"},
				{Key: "model", Env: "CODEX_MODEL", Help: "model override"},
				{Key: "effort", Env: "CODEX_EFFORT", Help: "reasoning effort override"},
				{Key: "stream", Env: "CODEX_STREAM", Help: "persistent app-server mode (false to disable)", Default: "true"},
				{Key: "dir", Env: "CODEX_DIR", Help: "working directory"},
				{Key: "kind", Env: "CODEX_KIND", Help: "backend kind"},
				// Env is deliberately left unbound (Env: "") — this setting comes
				// from the host per session, never from a daemon environment
				// variable. Binding it would let a single env var hijack every
				// session at once.
				{Key: "env", Env: "", Help: "per-session env injected at spawn (K=V per line); host-supplied, never from the environment"},
			},
			Models: Models,
			// The app-server asks before each command and each patch, one request
			// per call, so a rule written per tool is honoured as written. The
			// exec transport has no such channel, which is why a session falling
			// back to it is warned about rather than silently gated.
			Capabilities: contracts.Capabilities{Gate: contracts.GrainTool},
		},
		Backend: func(ctx context.Context, cfg contracts.PluginConfig) (contracts.Backend, error) {
			return NewBackend(ctx, Config{
				Kind:     cfg.Get("kind"),
				Stream:   cfg.Get("stream") != "false",
				Cmd:      cfg.Get("cmd"),
				Model:    cfg.Get("model"),
				Effort:   cfg.Get("effort"),
				Dir:      cfg.Get("dir"),
				ResumeID: cfg.Get("resume"),
				Env:      contracts.ParseEnvSetting(cfg.Get("env")),
			})
		},
	})
}
