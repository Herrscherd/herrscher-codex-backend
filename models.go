package codex

import contracts "github.com/Herrscherd/herrscher-contracts"

var efforts = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

// Models is the catalog of the OpenAI runtime. IDs and labels are drawn from
// MODEL_CATALOG.codex in the app, which becomes obsolete once this source
// takes over. Prices are absent (0 = unknown): they already were on the app
// side.
var Models = []contracts.ModelSpec{
	{ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol", Arg: "gpt-5.6-sol", Efforts: efforts, Route: contracts.RouteNative},
	{ID: "gpt-5.6-terra", Label: "GPT-5.6 Terra", Arg: "gpt-5.6-terra", Efforts: efforts, Route: contracts.RouteNative},
	{ID: "gpt-5.6-luna", Label: "GPT-5.6 Luna", Arg: "gpt-5.6-luna", Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Route: contracts.RouteNative},
	{ID: "gpt-5-codex", Label: "GPT-5 Codex", Arg: "gpt-5-codex", Route: contracts.RouteNative},
	{ID: "gpt-5.5", Label: "GPT-5.5", Arg: "gpt-5.5", Route: contracts.RouteNative},
	{ID: "o4-mini", Label: "o4-mini", Arg: "o4-mini", Route: contracts.RouteNative},

	{ID: "gw-gpt-5.6-sol", Label: "GPT-5.6 Sol", Arg: "gpt-5.6-sol", Efforts: efforts, Route: contracts.RouteGateway},
	{ID: "gw-gpt-5.5", Label: "GPT-5.5", Arg: "gpt-5.5", Route: contracts.RouteGateway},
}
