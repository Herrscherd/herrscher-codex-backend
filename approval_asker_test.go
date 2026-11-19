package codex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const fakeAppServerScript = `read -r _; printf '{"id":1,"result":{}}\n'; read -r _; read -r _; printf '%s\n' "$@" > "$RECORD"; printf '{"id":2,"result":{"thread":{"id":"thread-1"}}}\n'; cat >/dev/null`

var (
	errNoRestart = errors.New("the test responder cannot restart its session")
	errNoWrite   = errors.New("the test stdin refuses writes")
)

func withAsker(ctx context.Context, asker contracts.ApprovalAsker) context.Context {
	return contracts.WithApprovalAsker(ctx, asker)
}

func answerWith(decision contracts.ApprovalDecision, seen *[]contracts.ApprovalRequest) contracts.ApprovalAsker {
	return func(_ context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
		if seen != nil {
			*seen = append(*seen, req)
		}
		return contracts.ApprovalAnswer{ID: req.ID, Decision: decision, Reason: "test"}
	}
}

func subProcessMustNotRun(t *testing.T) func(context.Context, string, []string) ([]byte, error) {
	t.Helper()
	return func(context.Context, string, []string) ([]byte, error) {
		t.Error("the sub-process approver ran while the turn carried a daemon asker")
		return []byte(`{"decision":"allow"}`), nil
	}
}

func closeLogged(t *testing.T, closeSession func() error) {
	t.Helper()
	if err := closeSession(); err != nil {
		t.Logf("closing the fake app-server: %v", err)
	}
}

func collectReplies(sent *[]map[string]any) func(map[string]any) error {
	return func(m map[string]any) error {
		*sent = append(*sent, m)
		return nil
	}
}

func commandApprovalMsg() map[string]any {
	return map[string]any{
		"method": "item/commandExecution/requestApproval",
		"params": map[string]any{"threadId": "t1", "turnId": "turn-1", "itemId": "item-1", "command": "rm -rf /"},
	}
}

func onlyDecision(t *testing.T, sent []map[string]any) string {
	t.Helper()
	if len(sent) != 1 {
		t.Fatalf("sent %d replies, want 1", len(sent))
	}
	decision, isText := mapField(sent[0], fieldResult)["decision"].(string)
	if !isText {
		t.Fatalf("the reply carries no decision: %v", sent[0])
	}
	return decision
}

func TestAskerAnswerDecidesTheTurn(t *testing.T) {
	cases := []struct {
		name     string
		decision contracts.ApprovalDecision
		want     string
	}{
		{name: "allow is accepted", decision: contracts.DecisionAllow, want: "accept"},
		{name: "deny is declined", decision: contracts.DecisionDeny, want: "decline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []contracts.ApprovalRequest
			var sent []map[string]any
			a := &approver{session: "api", ask: subProcessMustNotRun(t)}
			ctx := withAsker(context.Background(), answerWith(tc.decision, &seen))
			if err := answerApproval(ctx, commandApprovalMsg(), float64(9), a, collectReplies(&sent)); err != nil {
				t.Fatal(err)
			}
			if got := onlyDecision(t, sent); got != tc.want {
				t.Fatalf("decision = %q, want %q", got, tc.want)
			}
			if len(seen) != 1 || seen[0].Tool != "Bash" || seen[0].Subject != "rm -rf /" || seen[0].ID == "" {
				t.Fatalf("the asker saw %+v, want one Bash request on rm -rf / with an id", seen)
			}
		})
	}
}

func TestAskerAnswersTheRequestWithoutAnyApprover(t *testing.T) {
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	if err := answerApproval(ctx, commandApprovalMsg(), float64(9), nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if got := onlyDecision(t, sent); got != "accept" {
		t.Fatalf("decision = %q, want accept", got)
	}
}

func TestAskerFailuresDeclineTheTurn(t *testing.T) {
	cases := map[string]contracts.ApprovalAsker{
		"the answer names no decision": func(_ context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
			return contracts.ApprovalAnswer{ID: req.ID}
		},
		"the answer names an unknown decision": func(_ context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
			return contracts.ApprovalAnswer{ID: req.ID, Decision: "maybe"}
		},
		"the answer is for another request": func(context.Context, contracts.ApprovalRequest) contracts.ApprovalAnswer {
			return contracts.ApprovalAnswer{ID: "another", Decision: contracts.DecisionAllow}
		},
	}
	for name, asker := range cases {
		t.Run(name, func(t *testing.T) {
			var sent []map[string]any
			a := &approver{session: "api", ask: subProcessMustNotRun(t)}
			if err := answerApproval(withAsker(context.Background(), asker), commandApprovalMsg(), float64(9), a, collectReplies(&sent)); err != nil {
				t.Fatal(err)
			}
			if got := onlyDecision(t, sent); got != "decline" {
				t.Fatalf("decision = %q, want decline", got)
			}
		})
	}
}

func TestAskerIsNotTrustedOnceTheTurnEnds(t *testing.T) {
	t.Run("the turn already ended", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		asked := false
		asker := func(_ context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
			asked = true
			return contracts.ApprovalAnswer{ID: req.ID, Decision: contracts.DecisionAllow}
		}
		var sent []map[string]any
		if err := answerApproval(withAsker(ctx, asker), commandApprovalMsg(), float64(9), nil, collectReplies(&sent)); err != nil {
			t.Fatal(err)
		}
		if asked {
			t.Fatal("the daemon was asked for a turn that had already ended")
		}
		if got := onlyDecision(t, sent); got != "decline" {
			t.Fatalf("decision = %q, want decline", got)
		}
	})
	t.Run("the turn ends while the daemon answers", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		asker := func(_ context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
			cancel()
			return contracts.ApprovalAnswer{ID: req.ID, Decision: contracts.DecisionAllow}
		}
		var sent []map[string]any
		if err := answerApproval(withAsker(ctx, asker), commandApprovalMsg(), float64(9), nil, collectReplies(&sent)); err != nil {
			t.Fatal(err)
		}
		if got := onlyDecision(t, sent); got != "decline" {
			t.Fatalf("decision = %q, want decline", got)
		}
	})
}

func TestUnrecognisedRequestIsDeclinedWithWarningWhenAnAskerIsPresent(t *testing.T) {
	const method = "item/somethingNew/requestApproval"
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	var warned strings.Builder
	cases := map[string]*approver{
		"no approver":   nil,
		"with approver": {session: "api", warn: &warned},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			var sent []map[string]any
			if err := answerApproval(ctx, map[string]any{"method": method}, float64(1), a, collectReplies(&sent)); err != nil {
				t.Fatal(err)
			}
			if got := onlyDecision(t, sent); got != "decline" {
				t.Fatalf("decision = %q, want decline", got)
			}
		})
	}
	if !strings.Contains(warned.String(), method) {
		t.Fatalf("no warning named %s: %q", method, warned.String())
	}
}

func captureStderr(t *testing.T, run func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeLogged(t, read.Close) })
	saved := os.Stderr
	os.Stderr = write
	run()
	os.Stderr = saved
	if closeErr := write.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	out, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestUnrecognisedRequestWarnsWhenNoApproverAnswersForAnAsker(t *testing.T) {
	const method = "item/somethingNew/requestApproval"
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	var sent []map[string]any
	warnings := captureStderr(t, func() {
		if err := answerApproval(ctx, map[string]any{"method": method}, float64(1), nil, collectReplies(&sent)); err != nil {
			t.Error(err)
		}
	})
	if got := onlyDecision(t, sent); got != "decline" {
		t.Fatalf("decision = %q, want decline", got)
	}
	if !strings.Contains(warnings, method) {
		t.Fatalf("no warning named %s without an approver: %q", method, warnings)
	}
}

func TestApprovalIDIsStablePerRequest(t *testing.T) {
	const method = "item/commandExecution/requestApproval"
	params := map[string]any{"threadId": "t1", "turnId": "turn-1", "itemId": "item-1"}
	first, err := approvalID(method, float64(9), params)
	if err != nil {
		t.Fatal(err)
	}
	again, err := approvalID(method, float64(9), params)
	if err != nil {
		t.Fatal(err)
	}
	other, err := approvalID(method, float64(10), params)
	if err != nil {
		t.Fatal(err)
	}
	if first != again || first == other {
		t.Fatalf("ids %q, %q and %q: a request keeps its id and two requests differ", first, again, other)
	}
}

func TestApprovalIDKeepsOnlyThePresentFields(t *testing.T) {
	id, err := approvalID("execCommandApproval", float64(7), map[string]any{"command": "ls"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "execCommandApproval/7" {
		t.Fatalf("id = %q, want execCommandApproval/7", id)
	}
}

func TestNullRequestIdIsDeclinedWithoutAskingTheAsker(t *testing.T) {
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	msg := map[string]any{"id": nil, "method": "execCommandApproval", "params": map[string]any{"command": "rm -rf /"}}
	if err := answerApproval(ctx, msg, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("the asker was called %d times for a request without id", len(seen))
	}
	if got := onlyDecision(t, sent); got != "decline" {
		t.Fatalf("decision = %q, want decline", got)
	}
}

func TestRequestFromAnotherThreadIsDeclinedWithoutAskingTheAsker(t *testing.T) {
	lines := `{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t2","turnId":"turn-2","itemId":"item-1","command":"rm -rf /"}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("the asker was called %d times for another thread", len(seen))
	}
	if got := onlyDecision(t, sent); got != "decline" {
		t.Fatalf("decision = %q, want decline", got)
	}
}

func TestRequestSentBeforeTheTurnIdIsKnownIsServedOnceLearned(t *testing.T) {
	lines := `{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-1","command":"ls"}}
{"id":7,"result":{"turn":{"id":"turn-1"}}}
{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1", requestID: 7}, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("the asker was called %d times, want 1 once the turn id was learned", len(seen))
	}
	if got := onlyDecision(t, sent); got != "accept" {
		t.Fatalf("decision = %q, want accept", got)
	}
}

func TestParkedRequestOfAnotherTurnIsDeclinedWithoutAskingTheAsker(t *testing.T) {
	lines := `{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-2","itemId":"item-1","command":"ls"}}
{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("the asker was called %d times for a request of another turn", len(seen))
	}
	if got := onlyDecision(t, sent); got != "decline" {
		t.Fatalf("decision = %q, want decline", got)
	}
}

func TestRequestsBeyondTheParkingBoundAreDeclinedWithoutAskingTheAsker(t *testing.T) {
	var lines []string
	for id := 1; id <= maxParkedRequests+1; id++ {
		lines = append(lines, fmt.Sprintf(`{"id":%d,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-%d","command":"ls"}}`, id, id))
	}
	lines = append(lines,
		`{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-1"}}}`,
		`{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}`,
	)
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(strings.Join(lines, "\n")+"\n")), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != maxParkedRequests {
		t.Fatalf("the asker was called %d times, want the %d parked requests", len(seen), maxParkedRequests)
	}
	if len(sent) != maxParkedRequests+1 {
		t.Fatalf("sent %d replies, want one per request (%d)", len(sent), maxParkedRequests+1)
	}
	if got := mapField(sent[0], fieldResult)["decision"]; got != "decline" {
		t.Fatalf("the request beyond the bound got %v first, want decline before the parked ones are served", got)
	}
}

func TestTurnEndingWithoutItsIdDeclinesItsParkedRequests(t *testing.T) {
	lines := `{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-1","command":"ls"}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent)); err == nil {
		t.Fatal("a turn that ends without its id should report the read failure")
	}
	if len(seen) != 0 {
		t.Fatalf("the asker was called %d times for a turn that never announced its id", len(seen))
	}
	if got := onlyDecision(t, sent); got != "decline" {
		t.Fatalf("decision = %q, want decline", got)
	}
}

func TestApprovalWithoutIdIsNotAnsweredNorAsked(t *testing.T) {
	lines := `{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
{"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-1","command":"rm -rf /"}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var seen []contracts.ApprovalRequest
	var sent []map[string]any
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, &seen))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{threadID: "t1"}, nil, nil, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 || len(sent) != 0 {
		t.Fatalf("a request without id reached the asker (%d) or got a reply (%d)", len(seen), len(sent))
	}
}

func TestReadTurnAnswersWithTheTurnAsker(t *testing.T) {
	lines := `{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","turnId":"turn-1","itemId":"item-1","command":"rm -rf /"}}
{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}
`
	var sent []map[string]any
	a := &approver{session: "api", ask: subProcessMustNotRun(t)}
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	if _, err := readTurn(ctx, bufio.NewReader(strings.NewReader(lines)), turnScope{}, nil, a, collectReplies(&sent)); err != nil {
		t.Fatal(err)
	}
	if got := onlyDecision(t, sent); got != "accept" {
		t.Fatalf("decision = %q, want accept", got)
	}
}

func launchRecorded(ctx context.Context, t *testing.T, approvals *approver) (string, *appSession) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "argv")
	session, err := startAppSession(context.Background(), ctx, []string{"sh", "-c", fakeAppServerScript}, "", "", t.TempDir(), false, "", map[string]string{"RECORD": record}, approvals)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeLogged(t, session.Close) })
	argv, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(argv), session
}

func TestAskerPresentAtLaunchStartsOnRequest(t *testing.T) {
	argv, session := launchRecorded(withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil)), t, nil)
	if !strings.Contains(argv, `approval_policy="on-request"`) {
		t.Fatalf("launched with %q, want on-request", argv)
	}
	if session.approvalsIfGated == nil {
		t.Fatal("the session launched on-request keeps no approver to answer its requests")
	}
}

func TestNoAskerNoGateStartsNever(t *testing.T) {
	argv, session := launchRecorded(context.Background(), t, nil)
	if !strings.Contains(argv, `approval_policy="never"`) {
		t.Fatalf("launched with %q, want never", argv)
	}
	if session.approvalsIfGated != nil {
		t.Fatal("an ungated session kept an approver")
	}
}

func TestGatedEnvironmentStartsOnRequestWithoutAsker(t *testing.T) {
	gate := &approver{session: "api", ask: subProcessMustNotRun(t)}
	argv, session := launchRecorded(context.Background(), t, gate)
	if !strings.Contains(argv, `approval_policy="on-request"`) {
		t.Fatalf("launched with %q, want on-request", argv)
	}
	if session.approvalsIfGated != gate {
		t.Fatal("the session did not keep the environment gate")
	}
}

func TestOneShotRefusesAnAskerTurnWithoutRunningTheCommand(t *testing.T) {
	ran := false
	o := &oneShotResponder{run: func(context.Context, contracts.Prompt) (string, error) {
		ran = true
		return "ran", nil
	}}
	_, err := o.Respond(withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil)), contracts.Prompt{}, nil)
	if !errors.Is(err, errUngatedSession) {
		t.Fatalf("err = %v, want errUngatedSession", err)
	}
	if ran {
		t.Fatal("the one-shot command ran for a turn that carried an asker")
	}
}

func TestOneShotRunsATurnWithoutAnAsker(t *testing.T) {
	o := &oneShotResponder{run: func(context.Context, contracts.Prompt) (string, error) {
		return "ran", nil
	}}
	text, err := o.Respond(context.Background(), contracts.Prompt{}, nil)
	if err != nil || text != "ran" {
		t.Fatalf("Respond = %q, %v; want ran, nil", text, err)
	}
}

func TestAskerTurnOnAnUngatedSessionIsRefused(t *testing.T) {
	r := &streamResponder{sess: newAppSession(brokenWriteCloser{err: errNoWrite}, strings.NewReader(""))}
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	if _, err := r.Respond(ctx, contracts.Prompt{}, nil); !errors.Is(err, errUngatedSession) {
		t.Fatalf("err = %v, want errUngatedSession", err)
	}
}

func TestAskerTurnOnAGatedSessionIsNotRefused(t *testing.T) {
	gated := newAppSession(brokenWriteCloser{err: errNoWrite}, strings.NewReader(""))
	gated.approvalsIfGated = &approver{session: "api"}
	r := &streamResponder{sess: gated, start: func(context.Context, string) (*appSession, error) {
		return nil, errNoRestart
	}}
	ctx := withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil))
	if _, err := r.Respond(ctx, contracts.Prompt{}, nil); errors.Is(err, errUngatedSession) {
		t.Fatalf("a gated session refused its turn: %v", err)
	}
}

func TestGateSurvivesARestartOfTheSession(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	r := &streamResponder{base: []string{"sh", "-c", fakeAppServerScript}, dir: t.TempDir(), env: map[string]string{"RECORD": record}}
	r.start = r.launchedWithin(context.Background())
	t.Cleanup(func() { closeLogged(t, r.Close) })
	if err := r.open(withAsker(context.Background(), answerWith(contracts.DecisionAllow, nil)), ""); err != nil {
		t.Fatal(err)
	}
	restarted, err := r.start(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeLogged(t, restarted.Close) })
	argv, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), `approval_policy="on-request"`) {
		t.Fatalf("the restarted session launched with %q, want on-request", argv)
	}
}
