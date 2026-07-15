package codex

import (
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

func TestSelfRegisteredAsBackend(t *testing.T) {
	for _, p := range contracts.Default.Backends() {
		if p.Manifest.Kind == "codex" {
			if p.Backend == nil {
				t.Fatal("Codex backend factory is nil")
			}
			return
		}
	}
	t.Fatal("Codex backend did not self-register")
}
