package codex

import (
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

func TestModelsAreValid(t *testing.T) {
	if err := contracts.ValidateModels("codex", Models); err != nil {
		t.Fatalf("codex model catalog is invalid: %v", err)
	}
}

func TestModelsAreAllNativeForNow(t *testing.T) {
	// Gateway entries are a later task. Until they land, this backend must only
	// declare native models — otherwise the host would offer a model that no
	// credential can serve.
	for _, m := range Models {
		if m.Route != contracts.RouteNative {
			t.Errorf("model %q has route %q, expected native at this stage", m.ID, m.Route)
		}
	}
}

func TestManifestPublishesModels(t *testing.T) {
	var found bool
	for _, p := range contracts.Default.Backends() {
		if p.Manifest.Kind != "codex" {
			continue
		}
		found = true
		if len(p.Manifest.Models) != len(Models) {
			t.Fatalf("manifest published %d models, catalog has %d", len(p.Manifest.Models), len(Models))
		}
	}
	if !found {
		t.Fatal("codex backend did not self-register")
	}
}
