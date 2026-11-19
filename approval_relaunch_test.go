package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const turnAppServerScript = `read -r _; printf '{"id":1,"result":{}}\n'; read -r _; read -r threadReq; printf '%s\n' "$threadReq" > "$RECORD.thread"; printf '%s\n' "$@" > "$RECORD"; printf '{"id":2,"result":{"thread":{"id":"thread-1"}}}\n'; read -r _; printf '{"id":3,"result":{"turn":{"id":"turn-1"}}}\n'; printf '{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}\n'; cat >/dev/null`

func turnResponder(t *testing.T, record, resume string) *streamResponder {
	t.Helper()
	r := &streamResponder{base: []string{"sh", "-c", turnAppServerScript}, dir: t.TempDir(), env: map[string]string{"RECORD": record}, resumeID: resume}
	r.start = r.launchedWithin(context.Background())
	t.Cleanup(func() { closeLogged(t, r.Close) })
	return r
}

func recordOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAskerTurnRelaunchesAnUngatedSessionWithItsThreadOnRequest(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	r := turnResponder(t, record, "")
	if _, err := r.Respond(context.Background(), contracts.Prompt{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recordOf(t, record), `approval_policy="never"`) {
		t.Fatal("the first turn did not start the session on never")
	}
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	if _, err := r.Respond(ctx, contracts.Prompt{}, nil); err != nil {
		t.Fatalf("the asker turn was refused instead of relaunching the session: %v", err)
	}
	if !strings.Contains(recordOf(t, record), `approval_policy="on-request"`) {
		t.Fatal("the relaunched session did not start on-request")
	}
	if thread := recordOf(t, record+".thread"); !strings.Contains(thread, "thread/resume") || !strings.Contains(thread, "thread-1") {
		t.Fatalf("the relaunch did not resume thread-1: %q", thread)
	}
	if r.sess.approvalsIfGated == nil {
		t.Fatal("the relaunched session keeps no approver")
	}
}

func TestAskerTurnWithoutAThreadIsRefusedWithoutRelaunch(t *testing.T) {
	r := &streamResponder{
		sess: newAppSession(brokenWriteCloser{err: errNoWrite}, strings.NewReader("")),
		start: func(context.Context, string) (*appSession, error) {
			t.Error("a session without a thread was relaunched")
			return nil, errNoRestart
		},
	}
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	if _, err := r.Respond(ctx, contracts.Prompt{}, nil); !errors.Is(err, errUngatedSession) {
		t.Fatalf("err = %v, want errUngatedSession", err)
	}
}

func TestSessionLaunchedByAnAskerRefusesALaterTurnWithoutAsker(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	r := turnResponder(t, record, "")
	if _, err := r.Respond(withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil)), contracts.Prompt{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Respond(context.Background(), contracts.Prompt{}, nil); !errors.Is(err, errApprovalChannelLost) {
		t.Fatalf("err = %v, want errApprovalChannelLost", err)
	}
}

func TestCompactFollowsTheSameTurnRulesAsRespond(t *testing.T) {
	asker := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	asked := contracts.CompactRequest{}
	cases := map[string]struct {
		responder *streamResponder
		asked     bool
		want      error
	}{
		"an asker on an ungated session without a thread": {
			responder: &streamResponder{sess: newAppSession(brokenWriteCloser{err: errNoWrite}, strings.NewReader(""))},
			asked:     true,
			want:      errUngatedSession,
		},
		"no asker on a session that an asker put on request": {
			responder: &streamResponder{sess: askerLaunchedSession()},
			want:      errApprovalChannelLost,
		},
	}
	plain := context.Background()
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := plain
			if tc.asked {
				ctx = asker
			}
			_, err := tc.responder.Compact(ctx, asked, nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func askerLaunchedSession() *appSession {
	session := newAppSession(brokenWriteCloser{err: errNoWrite}, strings.NewReader(""))
	session.approvalsIfGated = &approver{session: "api"}
	return session
}

func TestReconstructedResponderWithAnAskerTurnResumesOnRequest(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	r := turnResponder(t, record, "thread-1")
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	if _, err := r.Respond(ctx, contracts.Prompt{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recordOf(t, record), `approval_policy="on-request"`) {
		t.Fatal("the reconstructed session did not start on-request")
	}
	if thread := recordOf(t, record+".thread"); !strings.Contains(thread, "thread/resume") {
		t.Fatalf("the reconstructed session did not resume its thread: %q", thread)
	}
}
