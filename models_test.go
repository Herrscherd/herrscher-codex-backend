package codex

import (
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
	for _, m := range Models() {
		if m.Route == contracts.RouteGateway {
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

func TestGPT6LunaAndTerraAndLunaAreNativeWithTheirOwnArg(t *testing.T) {
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

func TestGPT6LunaHasNoUltraEffort(t *testing.T) {
	spec, ok := findModel("gpt-6-luna")
	if !ok {
		t.Fatal("gpt-6-luna missing from the catalog")
	}
	for _, effort := range spec.Efforts {
		if effort == "ultra" {
			t.Fatalf("gpt-6-luna offers effort ultra: %v", spec.Efforts)
		}
	}
}

func findModel(id string) (contracts.ModelSpec, bool) {
	for _, spec := range Models() {
		if spec.ID == id {
			return spec, true
		}
	}
	return contracts.ModelSpec{}, false
}
