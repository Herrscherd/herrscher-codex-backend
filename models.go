package codex

import contracts "github.com/Herrscherd/herrscher-contracts"

const (
	modelGPT6Luna   = "gpt-6-luna"
	modelGPT56Sol   = "gpt-5.6-sol"
	modelGPT56Terra = "gpt-5.6-terra"
	modelGPT56Luna  = "gpt-5.6-luna"
	modelGPT55      = "gpt-5.5"
	modelO4Mini     = "o4-mini"
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
		{ID: modelGPT6Luna, Label: "GPT-6 Luna", Arg: modelGPT6Luna, Efforts: codexEffortsWithoutUltra(), Route: contracts.RouteNative},
		{ID: modelGPT56Sol, Label: "GPT-5.6 Sol (ancien)", Arg: modelGPT56Sol, Efforts: codexEfforts(), Route: contracts.RouteNative},
		{ID: modelGPT56Terra, Label: "GPT-5.6 Terra", Arg: modelGPT56Terra, Efforts: codexEfforts(), Route: contracts.RouteNative},
		{ID: modelGPT56Luna, Label: "GPT-5.6 Luna", Arg: modelGPT56Luna, Efforts: codexEffortsWithoutUltra(), Route: contracts.RouteNative},
		{ID: "gpt-5-codex", Label: "GPT-5 Codex (ancien)", Arg: "gpt-5-codex", Route: contracts.RouteNative},
		{ID: modelGPT55, Label: "GPT-5.5 (ancien)", Arg: modelGPT55, Route: contracts.RouteNative},
		{ID: modelO4Mini, Label: "o4-mini (ancien)", Arg: modelO4Mini, Route: contracts.RouteNative},

		{ID: "gw-gpt-5.6-sol", Label: "GPT-5.6 Sol", Arg: modelGPT56Sol, Efforts: codexEfforts(), Route: contracts.RouteGateway},
		{ID: "gw-gpt-5.5", Label: "GPT-5.5", Arg: modelGPT55, Route: contracts.RouteGateway},
	}
}
