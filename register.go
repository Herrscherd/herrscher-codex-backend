package codex

import (
	"context"

	"github.com/Herrscherd/herrscher-contracts"
)

func init() {
	contracts.Register(contracts.Plugin{
		Manifest: contracts.Manifest{
			Kind: "codex", Category: contracts.CategoryBackend,
			Config: []contracts.Setting{
				{Key: "cmd", Env: "CODEX_CMD", Help: "base command to run the agent", Default: "codex"},
				{Key: "model", Env: "CODEX_MODEL", Help: "model override"},
				{Key: "effort", Env: "CODEX_EFFORT", Help: "reasoning effort override"},
				{Key: "stream", Env: "CODEX_STREAM", Help: "persistent app-server mode (false to disable)", Default: "true"},
				{Key: "dir", Env: "CODEX_DIR", Help: "working directory"},
				{Key: "kind", Env: "CODEX_KIND", Help: "backend kind"},
			},
		},
		Backend: func(ctx context.Context, cfg contracts.PluginConfig) (contracts.Backend, error) {
			return NewBackend(ctx, Config{Kind: cfg.Get("kind"), Stream: cfg.Get("stream") != "false", Cmd: cfg.Get("cmd"), Model: cfg.Get("model"), Effort: cfg.Get("effort"), Dir: cfg.Get("dir")})
		},
	})
}
