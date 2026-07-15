package codex

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestAppSessionLiveTwoTurns(t *testing.T) {
	if os.Getenv("DCTL_LIVE") != "1" {
		t.Skip("set DCTL_LIVE=1 to run the live Codex smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	s, err := startAppSession(ctx, []string{"codex"}, "", "", ".", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.Send("Reply with exactly one word: ONE", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Send("Reply with exactly one word: TWO", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Text == "" || second.Text == "" || s.threadID == "" {
		t.Fatalf("unexpected live results: %+v %+v thread=%q", first, second, s.threadID)
	}
}
