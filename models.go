package codex

import contracts "github.com/Herrscherd/herrscher-contracts"

const (
	modelGPT6Luna   = "gpt-6-luna"
	modelGPT61Sol   = "gpt-6.1-sol"
	modelGPT61Terra = "gpt-6.1-terra"
	modelGPT56Sol   = "gpt-5.6-sol"
	modelGPT56Terra = "gpt-5.6-terra"
	modelGPT56Luna  = "gpt-5.6-luna"
	modelGPT55      = "gpt-5.5"

	context1050K = 1_050_000

	gpt6LunaInput        = 0.1
	gpt6LunaOutput       = 0.5
	gpt6LunaCacheRead    = 0.01
	gpt6LunaCacheWrite   = 0.125
	gpt61SolInput        = 2
	gpt61SolOutput       = 10
	gpt61SolCacheRead    = 0.1
	gpt61SolCacheWrite   = 2.5
	gpt61TerraInput      = 1
	gpt61TerraOutput     = 5
	gpt61TerraCacheRead  = 0.05
	gpt61TerraCacheWrite = 1.25
	gpt56SolInput        = 4
	gpt56SolOutput       = 20
	gpt56SolCacheRead    = 0.4
	gpt56SolCacheWrite   = 5
	gpt56TerraInput      = 2
	gpt56TerraOutput     = 12
	gpt56TerraCacheRead  = 0.2
	gpt56TerraCacheWrite = 2.5
	gpt56LunaInput       = 0.2
	gpt56LunaOutput      = 1.2
	gpt56LunaCacheRead   = 0.02
	gpt56LunaCacheWrite  = 0.25
	gpt55Input           = 5
	gpt55Output          = 30
	gpt55CacheRead       = 0.5
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
		{ID: modelGPT6Luna, Label: "GPT-6 Luna", Arg: modelGPT6Luna, Efforts: codexEffortsWithoutUltra(), Route: contracts.RouteNative, InputPrice: gpt6LunaInput, OutputPrice: gpt6LunaOutput, CacheReadPrice: gpt6LunaCacheRead, CacheWritePrice: gpt6LunaCacheWrite, ContextWindow: context1050K},
		{ID: modelGPT61Sol, Label: "GPT-6.1 Sol", Arg: modelGPT61Sol, Efforts: codexEffortsWithoutUltra(), Route: contracts.RouteNative, InputPrice: gpt61SolInput, OutputPrice: gpt61SolOutput, CacheReadPrice: gpt61SolCacheRead, CacheWritePrice: gpt61SolCacheWrite, ContextWindow: context1050K},
		{ID: modelGPT61Terra, Label: "GPT-6.1 Terra", Arg: modelGPT61Terra, Efforts: codexEffortsWithoutUltra(), Route: contracts.RouteNative, InputPrice: gpt61TerraInput, OutputPrice: gpt61TerraOutput, CacheReadPrice: gpt61TerraCacheRead, CacheWritePrice: gpt61TerraCacheWrite, ContextWindow: context1050K},
		{ID: modelGPT56Sol, Label: "GPT-5.6 Sol (ancien)", Arg: modelGPT56Sol, Efforts: codexEfforts(), Route: contracts.RouteNative, InputPrice: gpt56SolInput, OutputPrice: gpt56SolOutput, CacheReadPrice: gpt56SolCacheRead, CacheWritePrice: gpt56SolCacheWrite, Hidden: true},
		{ID: modelGPT56Terra, Label: "GPT-5.6 Terra", Arg: modelGPT56Terra, Efforts: codexEfforts(), Route: contracts.RouteNative, InputPrice: gpt56TerraInput, OutputPrice: gpt56TerraOutput, CacheReadPrice: gpt56TerraCacheRead, CacheWritePrice: gpt56TerraCacheWrite},
		{ID: modelGPT56Luna, Label: "GPT-5.6 Luna", Arg: modelGPT56Luna, Efforts: codexEffortsWithoutUltra(), Route: contracts.RouteNative, InputPrice: gpt56LunaInput, OutputPrice: gpt56LunaOutput, CacheReadPrice: gpt56LunaCacheRead, CacheWritePrice: gpt56LunaCacheWrite},
		{ID: "gpt-5-codex", Label: "GPT-5 Codex (ancien)", Arg: "gpt-5-codex", Route: contracts.RouteNative, Hidden: true},
		{ID: modelGPT55, Label: "GPT-5.5 (ancien)", Arg: modelGPT55, Route: contracts.RouteNative, InputPrice: gpt55Input, OutputPrice: gpt55Output, CacheReadPrice: gpt55CacheRead, Hidden: true},
		{ID: "o4-mini", Label: "o4-mini (ancien)", Arg: "o4-mini", Route: contracts.RouteNative, Hidden: true},
		{ID: "gw-gpt-5.6-sol", Label: "GPT-5.6 Sol", Arg: modelGPT56Sol, Efforts: codexEfforts(), Route: contracts.RouteGateway, Hidden: true},
		{ID: "gw-gpt-5.5", Label: "GPT-5.5", Arg: modelGPT55, Route: contracts.RouteGateway, Hidden: true},
	}
}
