package codex

import contracts "github.com/Herrscherd/herrscher-contracts"

const (
	modelGPT6Luna   = "gpt-6-luna"
	modelGPT56Sol   = "gpt-5.6-sol"
	modelGPT56Terra = "gpt-5.6-terra"
	modelGPT56Luna  = "gpt-5.6-luna"
	modelGPT55      = "gpt-5.5"
	modelO4Mini     = "o4-mini"

	context1050K = 1_050_000
	context400K  = 400_000
	context200K  = 200_000

	gpt6LunaInput       = 0.1
	gpt6LunaOutput      = 0.5
	gpt6LunaCacheRead   = 0.01
	gpt56SolInput       = 4
	gpt56SolOutput      = 20
	gpt56SolCacheRead   = 0.4
	gpt56TerraInput     = 2
	gpt56TerraOutput    = 12
	gpt56TerraCacheRead = 0.2
	gpt56LunaInput      = 0.2
	gpt56LunaOutput     = 1.2
	gpt56LunaCacheRead  = 0.02
	gpt5CodexInput      = 1.25
	gpt5CodexOutput     = 10
	gpt5CodexCacheRead  = 0.125
	gpt55Input          = 5
	gpt55Output         = 30
	gpt55CacheRead      = 0.5
	o4MiniInput         = 1.1
	o4MiniOutput        = 4.4
	o4MiniCacheRead     = 0.275
)

func codexEfforts() []string {
	return []string{"low", "medium", "high", "xhigh", "max", "ultra"}
}

func codexEffortsWithoutUltra() []string {
	return []string{"low", "medium", "high", "xhigh", "max"}
}

// Models lists the models the host can route to this backend: the native ones on the
// machine's own subscription, and the gateway ones routed through the gateway.
func Models() []contracts.ModelSpec {
	return []contracts.ModelSpec{
		{ID: modelGPT6Luna, Label: "GPT-6 Luna", Arg: modelGPT6Luna, Efforts: codexEffortsWithoutUltra(), Route: contracts.RouteNative, InputPrice: gpt6LunaInput, OutputPrice: gpt6LunaOutput, CacheReadPrice: gpt6LunaCacheRead, ContextWindow: context1050K},
		{ID: modelGPT56Sol, Label: "GPT-5.6 Sol (ancien)", Arg: modelGPT56Sol, Efforts: codexEfforts(), Route: contracts.RouteNative, InputPrice: gpt56SolInput, OutputPrice: gpt56SolOutput, CacheReadPrice: gpt56SolCacheRead, ContextWindow: context1050K, Hidden: true},
		{ID: modelGPT56Terra, Label: "GPT-5.6 Terra", Arg: modelGPT56Terra, Efforts: codexEfforts(), Route: contracts.RouteNative, InputPrice: gpt56TerraInput, OutputPrice: gpt56TerraOutput, CacheReadPrice: gpt56TerraCacheRead, ContextWindow: context1050K},
		{ID: modelGPT56Luna, Label: "GPT-5.6 Luna", Arg: modelGPT56Luna, Efforts: codexEffortsWithoutUltra(), Route: contracts.RouteNative, InputPrice: gpt56LunaInput, OutputPrice: gpt56LunaOutput, CacheReadPrice: gpt56LunaCacheRead, ContextWindow: context1050K},
		{ID: "gpt-5-codex", Label: "GPT-5 Codex (ancien)", Arg: "gpt-5-codex", Route: contracts.RouteNative, InputPrice: gpt5CodexInput, OutputPrice: gpt5CodexOutput, CacheReadPrice: gpt5CodexCacheRead, ContextWindow: context400K, Hidden: true},
		{ID: modelGPT55, Label: "GPT-5.5 (ancien)", Arg: modelGPT55, Route: contracts.RouteNative, InputPrice: gpt55Input, OutputPrice: gpt55Output, CacheReadPrice: gpt55CacheRead, ContextWindow: context1050K, Hidden: true},
		{ID: modelO4Mini, Label: "o4-mini (ancien)", Arg: modelO4Mini, Route: contracts.RouteNative, InputPrice: o4MiniInput, OutputPrice: o4MiniOutput, CacheReadPrice: o4MiniCacheRead, ContextWindow: context200K, Hidden: true},

		{ID: "gw-gpt-5.6-sol", Label: "GPT-5.6 Sol", Arg: modelGPT56Sol, Efforts: codexEfforts(), Route: contracts.RouteGateway, Hidden: true},
		{ID: "gw-gpt-5.5", Label: "GPT-5.5", Arg: modelGPT55, Route: contracts.RouteGateway, Hidden: true},
	}
}
