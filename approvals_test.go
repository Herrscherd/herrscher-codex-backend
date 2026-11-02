package codex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func gatedEnv(session string) func(string) string {
	return func(k string) string {
		switch k {
		case contracts.EnvApprovalsMode:
			return "strict"
		case contracts.EnvApprovalsSession:
			return session
		case contracts.EnvApprovalsBin:
			return "/opt/herrscher"
		}
		return ""
	}
}

func TestNewApproverNeedsTheWholeEnvironment(t *testing.T) {
	if newApprover(gatedEnv("api")) == nil {
		t.Fatal("a complete environment produced no approver")
	}
	for _, drop := range []string{contracts.EnvApprovalsMode, contracts.EnvApprovalsSession, contracts.EnvApprovalsBin} {
		full := gatedEnv("api")
		if newApprover(func(k string) string {
			if k == drop {
				return ""
			}
			return full(k)
		}) != nil {
			t.Fatalf("missing %s still produced an approver", drop)
		}
	}
	if newApprover(func(k string) string {
		if k == contracts.EnvApprovalsMode {
			return "bypass"
		}
		return gatedEnv("api")(k)
	}) != nil {
		t.Fatal("bypass produced an approver")
	}
}

func TestAllowAsksTheDaemonAndReadsItsVerdict(t *testing.T) {
	var got []string
	a := &approver{session: "api", bin: "/opt/herrscher", ask: func(_ context.Context, _ string, argv []string) ([]byte, error) {
		got = argv
		return []byte(`{"decision":"deny","reason":"no"}`), nil
	}}
	if a.allow(context.Background(), "Bash", "git push --force") {
		t.Fatal("a denied call was allowed")
	}
	want := []string{"approve", "ask", "--session", "api", "--tool", "Bash", "--subject", "git push --force"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

func TestAllowFailsOpen(t *testing.T) {
	cases := map[string]func(context.Context, string, []string) ([]byte, error){
		"the daemon could not be reached": func(context.Context, string, []string) ([]byte, error) {
			return nil, context.DeadlineExceeded
		},
		"the answer was not json": func(context.Context, string, []string) ([]byte, error) {
			return []byte("what"), nil
		},
		"the answer named no decision": func(context.Context, string, []string) ([]byte, error) {
			return []byte(`{}`), nil
		},
	}
	for name, ask := range cases {
		t.Run(name, func(t *testing.T) {
			a := &approver{session: "api", bin: "/opt/herrscher", ask: ask}
			if !a.allow(context.Background(), "Bash", "ls") {
				t.Fatal("an internal failure denied a tool call")
			}
		})
	}
	var nilApprover *approver
	if !nilApprover.allow(context.Background(), "Bash", "ls") {
		t.Fatal("an ungated session denied a tool call")
	}
}

func TestApprovalSubjectCoversBothSpellings(t *testing.T) {
	for _, m := range []string{"item/commandExecution/requestApproval", "execCommandApproval"} {
		req, ok := approvalSubject(m, map[string]any{"command": "rm -rf /"})
		if !ok || req.tool != "Bash" || req.subject != "rm -rf /" {
			t.Fatalf("%s = (%+v, %v)", m, req, ok)
		}
	}
	changes := map[string]any{"b.go": make(map[string]any), "a.go": make(map[string]any)}
	for _, m := range []string{"item/fileChange/requestApproval", "applyPatchApproval"} {
		req, ok := approvalSubject(m, map[string]any{"fileChanges": changes})
		if !ok || req.tool != "Write" || req.subject != "a.go b.go" {
			t.Fatalf("%s = (%+v, %v)", m, req, ok)
		}
	}
	if _, ok := approvalSubject("item/somethingNew/requestApproval", nil); ok {
		t.Fatal("an unknown method was claimed as known")
	}
}

func TestApprovalResponseDeclinesRatherThanCancelling(t *testing.T) {
	deny := approvalResponse(7, false)
	if deny["id"] != 7 {
		t.Fatalf("answered on id %v, want 7", deny["id"])
	}
	if got := mapField(deny, "result")["decision"]; got != "decline" {
		t.Fatalf("decision = %v, want decline", got)
	}
	if got := mapField(approvalResponse(7, true), "result")["decision"]; got != "accept" {
		t.Fatalf("decision = %v, want accept", got)
	}
}

func TestAnswerApprovalAlwaysReplies(t *testing.T) {
	var sent []map[string]any
	respond := func(m map[string]any) error { sent = append(sent, m); return nil }
	a := &approver{session: "api", ask: func(context.Context, string, []string) ([]byte, error) {
		return []byte(`{"decision":"deny"}`), nil
	}}
	if err := answerApproval(context.Background(), map[string]any{"method": "item/somethingNew/requestApproval"}, 1, a, respond); err != nil {
		t.Fatal(err)
	}
	if err := answerApproval(context.Background(), map[string]any{
		"method": "item/commandExecution/requestApproval",
		"params": map[string]any{"command": "rm -rf /"},
	}, 2, a, respond); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent %d replies, want 2", len(sent))
	}
	if got := mapField(sent[0], "result")["decision"]; got != "accept" {
		t.Fatalf("unknown method answered %v, want accept", got)
	}
	if got := mapField(sent[1], "result")["decision"]; got != "decline" {
		t.Fatalf("denied command answered %v, want decline", got)
	}
}

func TestApprovalPolicyFollowsTheGate(t *testing.T) {
	if got := approvalPolicy(true); got != "on-request" {
		t.Fatalf("gated policy = %q", got)
	}
	if got := approvalPolicy(false); got != "never" {
		t.Fatalf("ungated policy = %q", got)
	}
	if !strings.Contains(strings.Join(appServerArgv([]string{"codex"}, true), " "), `approval_policy="on-request"`) {
		t.Fatal("a gated app-server was started with nothing to ask about")
	}
}

func TestWarnOneShotUngatedSpeaksOnlyWhenAPolicyExists(t *testing.T) {
	var buf bytes.Buffer
	warnOneShotUngated(nil, &buf)
	if buf.Len() != 0 {
		t.Fatalf("warned about a session that asked for nothing: %q", buf.String())
	}
	warnOneShotUngated(&approver{session: "api"}, &buf)
	if !strings.Contains(buf.String(), "ungated") || !strings.Contains(buf.String(), "api") {
		t.Fatalf("the warning names neither the session nor what happens to it: %q", buf.String())
	}
}

func TestDeclaresPerToolGating(t *testing.T) {
	for _, p := range contracts.Default.Backends() {
		if p.Manifest.Kind != "codex" {
			continue
		}
		if p.Manifest.Capabilities.Gate != contracts.GrainTool {
			t.Fatalf("gate = %q, want tool", p.Manifest.Capabilities.Gate)
		}
		return
	}
	t.Fatal("codex backend did not self-register into contracts.Default")
}

func TestReadTurnAnswersAnApprovalRequestMidTurn(t *testing.T) {
	lines := `{"id":9,"method":"item/commandExecution/requestApproval","params":{"command":"rm -rf /"}}
{"method":"turn/completed","params":{"threadId":"t1"}}
`
	var sent []map[string]any
	a := &approver{session: "api", ask: func(context.Context, string, []string) ([]byte, error) {
		return []byte(`{"decision":"deny"}`), nil
	}}
	if _, err := readTurn(context.Background(), bufio.NewReader(strings.NewReader(lines)), nil, a,
		func(m map[string]any) error { sent = append(sent, m); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d replies to one request", len(sent))
	}
	if sent[0]["id"] != float64(9) {
		t.Fatalf("answered on id %v, want the request's own 9", sent[0]["id"])
	}
	if got := mapField(sent[0], "result")["decision"]; got != "decline" {
		t.Fatalf("decision = %v, want decline", got)
	}
}

func TestAnswerApprovalWarnsOnceOnUnrecognisedApprovalRequest(t *testing.T) {
	cases := []struct {
		name   string
		method string
		want   bool
	}{
		{name: "renamed approval request", method: "item/somethingNew/requestApproval", want: true},
		{name: "legacy spelling", method: "someNewApproval", want: true},
		{name: "not an approval request", method: "auth/loginCallback", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var warned strings.Builder
			a := &approver{session: "api", warn: &warned}
			respond := func(map[string]any) error { return nil }
			if err := answerApproval(context.Background(), map[string]any{"method": tc.method}, 1, a, respond); err != nil {
				t.Fatal(err)
			}
			if err := answerApproval(context.Background(), map[string]any{"method": tc.method}, 2, a, respond); err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(warned.String(), tc.method)
			if got != tc.want {
				t.Fatalf("warned=%v want=%v (%q)", got, tc.want, warned.String())
			}
			if tc.want && strings.Count(warned.String(), "\n") != 1 {
				t.Fatalf("want a single warning, got %q", warned.String())
			}
		})
	}
}

func TestAnswerApprovalReturnsRespondError(t *testing.T) {
	sentinel := errors.New("respond failed")
	a := &approver{session: "api", ask: func(context.Context, string, []string) ([]byte, error) {
		return []byte(`{"decision":"allow"}`), nil
	}}
	respond := func(map[string]any) error { return sentinel }
	if err := answerApproval(context.Background(), map[string]any{"method": "item/somethingNew/requestApproval"}, 1, a, respond); !errors.Is(err, sentinel) {
		t.Fatalf("err=%v, want the respond error", err)
	}
}

func TestWriteWarningLogsFailedWrite(_ *testing.T) {
	writeWarning(failingWriter{}, "herrscher: a warning nobody reads\n")
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("sink closed") }
