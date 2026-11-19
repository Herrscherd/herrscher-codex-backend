//go:build unix

package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const failingRelaunchAppServerScript = `if [ -e "$MARK" ]; then echo "$$" > "$RECORD.pid"; exit 1; fi; : > "$MARK"; ` + turnAppServerScript

func TestFailedRelaunchThroughStartAppSessionClosesTheChildAndKeepsTheThread(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "argv")
	marker := filepath.Join(dir, "marker")
	r := &streamResponder{base: []string{"sh", "-c", failingRelaunchAppServerScript}, dir: dir, env: map[string]string{"RECORD": record, "MARK": marker}}
	r.start = r.launchedWithin(context.Background())
	t.Cleanup(func() { closeLogged(t, r.Close) })
	if _, err := r.Respond(context.Background(), contracts.Prompt{}, nil); err != nil {
		t.Fatal(err)
	}
	previous := r.sess
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	if _, err := r.Respond(ctx, contracts.Prompt{}, nil); err == nil {
		t.Fatal("the relaunch against a codex that fails to initialise succeeded")
	}
	if r.sess != nil {
		t.Fatal("a session is still held after a failed relaunch")
	}
	if previous.cmd.ProcessState == nil {
		t.Fatal("the previous codex process was not closed by the relaunch")
	}
	requireReaped(t, recordedPID(t, record+".pid"))
	if token := r.ResumeToken(); token != "thread-1" {
		t.Fatalf("ResumeToken() = %q after a failed relaunch, want thread-1", token)
	}
	if r.resumeID != "thread-1" {
		t.Fatalf("resumeID = %q after a failed relaunch, want thread-1", r.resumeID)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Respond(ctx, contracts.Prompt{}, nil); err != nil {
		t.Fatalf("the next asker turn did not relaunch: %v", err)
	}
	if !strings.Contains(recordOf(t, record), `approval_policy="on-request"`) {
		t.Fatal("the next asker turn did not start on-request")
	}
	if thread := recordOf(t, record+".thread"); !strings.Contains(thread, "thread/resume") || !strings.Contains(thread, "thread-1") {
		t.Fatalf("the next asker turn did not resume thread-1: %q", thread)
	}
}

func recordedPID(t *testing.T, path string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(recordOf(t, path)))
	if err != nil {
		t.Fatalf("the failing codex recorded no pid: %v", err)
	}
	return pid
}

func requireReaped(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the failing codex process %d is still alive: %v", pid, err)
	}
}
