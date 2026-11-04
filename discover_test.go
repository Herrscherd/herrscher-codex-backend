package codex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	fakeAppServerEnv  = "HERRSCHER_FAKE_APP_SERVER"
	fakeHeartbeatEnv  = "HERRSCHER_FAKE_HEARTBEAT"
	fakeModeEnv       = "HERRSCHER_FAKE_MODE"
	heartbeatModeName = "heartbeat"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeAppServerEnv) == "1" {
		fakeAppServerMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func fakeCodexCommand(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeAppServerEnv, "1")
	t.Setenv(fakeModeEnv, mode)
	return self
}

func envWith(command string) func(string) string {
	return func(name string) string {
		if name == "CODEX_CMD" {
			return command
		}
		return ""
	}
}

func TestDiscoverListsVisibleModelsAcrossPages(t *testing.T) {
	command := fakeCodexCommand(t, "pages")
	models, err := discover(context.Background(), contracts.DiscoveryAccess{Getenv: envWith(command)})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gpt-7" || models[1].ID != "gpt-8" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Label != "GPT-7" || models[0].Arg != "gpt-7" || models[0].Route != contracts.RouteNative {
		t.Fatalf("first model = %+v", models[0])
	}
	if strings.Join(models[0].Efforts, ",") != "low,high" {
		t.Fatalf("efforts = %v", models[0].Efforts)
	}
	if models[1].Label != "" || models[1].Efforts != nil {
		t.Fatalf("second model = %+v, want no label and no efforts", models[1])
	}
}

func TestDiscoverReportsRPCError(t *testing.T) {
	command := fakeCodexCommand(t, "rpcerror")
	_, err := discover(context.Background(), contracts.DiscoveryAccess{Getenv: envWith(command)})
	if !errors.Is(err, errCodexRPC) {
		t.Fatalf("err = %v, want errCodexRPC", err)
	}
}

func TestDiscoverMissingBinaryIsAnError(t *testing.T) {
	_, err := discover(context.Background(), contracts.DiscoveryAccess{Getenv: envWith(filepath.Join(t.TempDir(), "absent"))})
	if err == nil {
		t.Fatal("missing binary listed models")
	}
}

func TestDiscoverStopsWhenDeadlinePasses(t *testing.T) {
	command := fakeCodexCommand(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := discover(ctx, contracts.DiscoveryAccess{Getenv: envWith(command)})
	if err == nil {
		t.Fatal("a hanging app-server listed models")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("discover took %v past its deadline", elapsed)
	}
}

func TestDiscoverRefusesOversizedFrame(t *testing.T) {
	command := fakeCodexCommand(t, "giant")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := discover(ctx, contracts.DiscoveryAccess{Getenv: envWith(command)})
	if !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("err = %v, want errFrameTooLarge", err)
	}
}

func TestDiscoverDeadlineKillsGrandchildHoldingStdout(t *testing.T) {
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	t.Setenv(fakeHeartbeatEnv, heartbeat)
	command := fakeCodexCommand(t, "grandchild")
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := discover(ctx, contracts.DiscoveryAccess{Getenv: envWith(command)})
	if err == nil {
		t.Fatal("a hanging app-server listed models")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("discover took %v past its 1.5s deadline", elapsed)
	}
	before := fileSize(t, heartbeat)
	if before == 0 {
		t.Fatal("grandchild never started")
	}
	time.Sleep(300 * time.Millisecond)
	if after := fileSize(t, heartbeat); after != before {
		t.Fatalf("grandchild still writing after the deadline: %d -> %d bytes", before, after)
	}
}

func TestDiscoverKeepsStderrOutOfErrorAndLogsIt(t *testing.T) {
	logged := captureLog(t)
	command := fakeCodexCommand(t, "flood")
	_, err := discover(context.Background(), contracts.DiscoveryAccess{Getenv: envWith(command)})
	if err == nil {
		t.Fatal("a silent app-server listed models")
	}
	if strings.Contains(err.Error(), "app-server boot") {
		t.Fatalf("stderr reached the returned error: %v", err)
	}
	if !strings.Contains(logged.String(), "app-server boot") {
		t.Fatalf("stderr not logged: %s", logged)
	}
	if logged.Len() > discoverStderrMax+1024 {
		t.Fatalf("log carries %d bytes, stderr is not bounded", logged.Len())
	}
}

func TestDiscoverLogsNoSecretLines(t *testing.T) {
	logged := captureLog(t)
	command := fakeCodexCommand(t, "leaky")
	_, err := discover(context.Background(), contracts.DiscoveryAccess{Getenv: envWith(command)})
	if err == nil {
		t.Fatal("a silent app-server listed models")
	}
	text := logged.String()
	if !strings.Contains(text, "app-server boot") {
		t.Fatalf("benign stderr line not logged: %s", text)
	}
	for _, secret := range []string{"sk-live-123", "Bearer", "api_key", "session token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("log carries %q: %s", secret, text)
		}
	}
}

func fakeAppServerMain() {
	mode := os.Getenv(fakeModeEnv)
	if mode == heartbeatModeName {
		beat(os.Getenv(fakeHeartbeatEnv))
	}
	serveFakeAppServer(os.Stdin, os.Stdout, mode)
}

func beat(path string) {
	for {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			panic(err)
		}
		if _, err := file.WriteString("beat\n"); err != nil {
			panic(err)
		}
		if err := file.Close(); err != nil {
			panic(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startHeartbeatChild() {
	self, err := os.Executable()
	if err != nil {
		panic(err)
	}
	child := exec.CommandContext(context.Background(), self)
	child.Env = append(os.Environ(), fakeAppServerEnv+"=1", fakeModeEnv+"="+heartbeatModeName)
	child.Stdout = os.Stdout
	if err := child.Start(); err != nil {
		panic(err)
	}
}

func reply(out *os.File, line string) {
	if _, err := out.WriteString(line + "\n"); err != nil {
		panic(err)
	}
}

func serveFakeAppServer(in, out *os.File, mode string) {
	switch mode {
	case "flood":
		if _, err := os.Stderr.WriteString("app-server boot\n" + strings.Repeat("x", 100000)); err != nil {
			panic(err)
		}
		return
	case "leaky":
		if _, err := os.Stderr.WriteString("app-server boot\nAuthorization: Bearer sk-live-123\napi_key=sk-xyz\nsession token=abc\n"); err != nil {
			panic(err)
		}
		return
	}
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, `"method":"initialize"`):
			reply(out, `{"id":0,"result":{"userAgent":"fake"}}`)
		case strings.Contains(line, `"method":"model/list"`):
			if mode == "grandchild" {
				startHeartbeatChild()
			}
			if mode == "hang" || mode == "grandchild" {
				time.Sleep(time.Hour)
			}
			answerModelList(out, mode, line)
		}
	}
}

func answerModelList(out *os.File, mode, request string) {
	if mode == "giant" {
		reply(out, strings.Repeat("x", discoverFrameMax+1))
		return
	}
	if mode == "rpcerror" {
		reply(out, `{"id":1,"error":{"code":-32000,"message":"no login"}}`)
		return
	}
	if strings.Contains(request, `"cursor":"next"`) {
		reply(out, `{"id":1,"result":{"data":[{"model":"gpt-8","displayName":"","hidden":false,"supportedReasoningEfforts":[]}],"nextCursor":null}}`)
		return
	}
	reply(out, `{"method":"notice","params":{}}`)
	reply(out, `{"id":1,"result":{"data":[{"model":"gpt-7","displayName":"GPT-7","hidden":false,"supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]},{"model":"gpt-secret","displayName":"Hidden","hidden":true,"supportedReasoningEfforts":[]}],"nextCursor":"next"}}`)
}
