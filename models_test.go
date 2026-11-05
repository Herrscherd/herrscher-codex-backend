package codex

import (
	"slices"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestModelsAreValid(t *testing.T) {
	if err := contracts.ValidateModels("codex", Models()); err != nil {
		t.Fatalf("codex model catalog is invalid: %v", err)
	}
}

func TestGatewayModelsExist(t *testing.T) {
	var n int
	models := Models()
	for index := range models {
		if models[index].Route == contracts.RouteGateway {
			n++
		}
	}
	if n == 0 {
		t.Fatal("no gateway models declared; the public build would have an empty catalog")
	}
}

func TestManifestPublishesModels(t *testing.T) {
	var found bool
	for _, p := range contracts.Default.Backends() {
		if p.Manifest.Kind != "codex" {
			continue
		}
		found = true
		if len(p.Manifest.Models) != len(Models()) {
			t.Fatalf("manifest published %d models, catalog has %d", len(p.Manifest.Models), len(Models()))
		}
	}
	if !found {
		t.Fatal("codex backend did not self-register")
	}
}

func TestRetiredModelsStayListedForResume(t *testing.T) {
	retired := []string{"gpt-5.6-sol", "gpt-5-codex", "gpt-5.5", "o4-mini"}
	for _, id := range retired {
		spec, ok := findModel(id)
		if !ok {
			t.Errorf("retired model %q dropped from the catalog: a resumed session could not find it", id)
			continue
		}
		if spec.Route != contracts.RouteNative || spec.Arg != id {
			t.Errorf("retired model %q: route %q arg %q, want native with arg %q", id, spec.Route, spec.Arg, id)
		}
	}
}

func TestListedCodexModelsAreNativeWithTheirOwnArg(t *testing.T) {
	for _, id := range []string{"gpt-6-luna", "gpt-5.6-terra", "gpt-5.6-luna"} {
		spec, ok := findModel(id)
		if !ok {
			t.Errorf("model %q missing from the catalog", id)
			continue
		}
		if spec.Route != contracts.RouteNative || spec.Arg != id {
			t.Errorf("model %q: route %q arg %q, want native with arg %q", id, spec.Route, spec.Arg, id)
		}
	}
}

func TestGPT6LunaFamilyEffortsAreExact(t *testing.T) {
	want := []string{"low", "medium", "high", "xhigh", "max"}
	for _, id := range []string{"gpt-6-luna", "gpt-5.6-luna"} {
		spec, ok := findModel(id)
		if !ok {
			t.Errorf("model %q missing from the catalog", id)
			continue
		}
		if !slices.Equal(spec.Efforts, want) {
			t.Errorf("model %q efforts = %v, want exactly %v", id, spec.Efforts, want)
		}
	}
}

func findModel(id string) (contracts.ModelSpec, bool) {
	models := Models()
	for index := range models {
		if models[index].ID == id {
			return models[index], true
		}
	}
	return contracts.ModelSpec{}, false
}

type publishedPricing struct {
	input, output, cacheRead float64
	window                   int
}

func TestNativePricingFollowsPublishedTable(t *testing.T) {
	want := map[string]publishedPricing{
		"gpt-6-luna":    {input: 0.1, output: 0.5, cacheRead: 0.01, window: 1_050_000},
		"gpt-6.1-sol":   {input: 2, output: 10, cacheRead: 0.1, window: 1_050_000},
		"gpt-6.1-terra": {input: 1, output: 5, cacheRead: 0.05, window: 1_050_000},
		"gpt-5.6-sol":   {input: 4, output: 20, cacheRead: 0.4, window: 1_050_000},
		"gpt-5.6-terra": {input: 2, output: 12, cacheRead: 0.2, window: 1_050_000},
		"gpt-5.6-luna":  {input: 0.2, output: 1.2, cacheRead: 0.02, window: 1_050_000},
		"gpt-5-codex":   {input: 1.25, output: 10, cacheRead: 0.125, window: 400_000},
		"gpt-5.5":       {input: 5, output: 30, cacheRead: 0.5, window: 1_050_000},
		"o4-mini":       {input: 1.1, output: 4.4, cacheRead: 0.275, window: 200_000},
	}
	models := Models()
	for index := range models {
		spec := models[index]
		if spec.Route != contracts.RouteNative {
			continue
		}
		price, ok := want[spec.ID]
		if !ok {
			t.Errorf("native model %q has no published pricing in the test table", spec.ID)
			continue
		}
		got := publishedPricing{spec.InputPrice, spec.OutputPrice, spec.CacheReadPrice, spec.ContextWindow}
		if got != price {
			t.Errorf("model %q pricing = %+v, published %+v", spec.ID, got, price)
		}
		delete(want, spec.ID)
	}
	for id := range want {
		t.Errorf("published model %q missing from the native catalog", id)
	}
}

func TestGatewayEntriesMirrorTheirNativeTwin(t *testing.T) {
	models := Models()
	for index := range models {
		gateway := models[index]
		if gateway.Route != contracts.RouteGateway {
			continue
		}
		native, ok := nativeTwin(models, gateway.Arg)
		if !ok {
			t.Errorf("gateway model %q has no native twin with arg %q", gateway.ID, gateway.Arg)
			continue
		}
		if gateway.InputPrice != native.InputPrice || gateway.OutputPrice != native.OutputPrice || gateway.CacheReadPrice != native.CacheReadPrice || gateway.CacheWritePrice != native.CacheWritePrice || gateway.ContextWindow != native.ContextWindow {
			t.Errorf("gateway model %q does not mirror native %q: %+v versus %+v", gateway.ID, native.ID, gateway, native)
		}
	}
}

func nativeTwin(models []contracts.ModelSpec, arg string) (contracts.ModelSpec, bool) {
	for index := range models {
		if models[index].Route == contracts.RouteNative && models[index].Arg == arg {
			return models[index], true
		}
	}
	return contracts.ModelSpec{}, false
}

func TestGPT61EffortsFollowThePage(t *testing.T) {
	want := []string{"low", "medium", "high", "xhigh", "max"}
	for _, id := range []string{"gpt-6.1-sol", "gpt-6.1-terra"} {
		spec, ok := findModel(id)
		if !ok {
			t.Errorf("model %q missing from the catalog", id)
			continue
		}
		if !slices.Equal(spec.Efforts, want) {
			t.Errorf("model %q efforts = %v, want exactly %v", id, spec.Efforts, want)
		}
	}
}

func TestRetiredAndSupersededModelsAreHidden(t *testing.T) {
	hidden := map[string]bool{
		"gpt-5.6-sol":    true,
		"gpt-5-codex":    true,
		"gpt-5.5":        true,
		"o4-mini":        true,
		"gw-gpt-5.6-sol": true,
		"gw-gpt-5.5":     true,
	}
	models := Models()
	for index := range models {
		spec := models[index]
		if spec.Hidden != hidden[spec.ID] {
			t.Errorf("model %q hidden = %v, want %v", spec.ID, spec.Hidden, hidden[spec.ID])
		}
	}
}
