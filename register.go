package codex

import (
	"context"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func init() {
	contracts.Register(contracts.Plugin{
		Manifest: contracts.Manifest{
			Kind: codexName, Category: contracts.CategoryBackend,
			Status: contracts.StatusLive,
			Config: []contracts.Setting{
				{Key: "cmd", Env: "CODEX_CMD", Help: "base command to run the agent", Default: codexName},
				{Key: "model", Env: "CODEX_MODEL", Help: "model override"},
				{Key: "effort", Env: "CODEX_EFFORT", Help: "reasoning effort override"},
				{Key: kindStream, Env: "CODEX_STREAM", Help: "persistent app-server mode (false to disable)", Default: "true"},
				{Key: "dir", Env: "CODEX_DIR", Help: "working directory"},
				{Key: "compactTimeout", Env: envCompactTimeout, Help: "deadline of one compaction, a Go duration such as 15m (default 10m)"},
				{Key: "kind", Env: "CODEX_KIND", Help: "backend kind"},
				{Key: "env", Env: "", Help: "per-session env injected at spawn (K=V per line); host-supplied, never from the environment"},
			},
			Models:       Models(),
			Capabilities: contracts.Capabilities{Gate: contracts.GrainTool, Compactor: true},
		},
		Discover: discover,
		Backend: func(ctx context.Context, cfg contracts.PluginConfig) (contracts.Backend, error) {
			compactTimeout, err := parseCompactTimeout(cfg.Get("compactTimeout"))
			if err != nil {
				return nil, err
			}
			return NewBackend(ctx, Config{
				Kind:           cfg.Get("kind"),
				Stream:         cfg.Get("stream") != "false",
				Cmd:            cfg.Get("cmd"),
				Model:          cfg.Get("model"),
				Effort:         cfg.Get("effort"),
				Dir:            cfg.Get("dir"),
				ResumeID:       cfg.Get("resume"),
				Env:            contracts.ParseEnvSetting(cfg.Get("env")),
				CompactTimeout: compactTimeout,
			})
		},
	})
}
