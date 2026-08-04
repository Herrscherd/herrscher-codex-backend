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

func TestGatewayModelsExist(t *testing.T) {
	var n int
	for _, m := range Models {
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
		if len(p.Manifest.Models) != len(Models) {
			t.Fatalf("manifest published %d models, catalog has %d", len(p.Manifest.Models), len(Models))
		}
	}
	if !found {
		t.Fatal("codex backend did not self-register")
	}
}
