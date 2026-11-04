package codex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	fakeAppServerEnv     = "HERRSCHER_FAKE_APP_SERVER"
	fakeGrandchildPIDEnv = "HERRSCHER_FAKE_GRANDCHILD_PID"
)

func processAlive(t *testing.T, pid int) bool {
	t.Helper()
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(stat))
	return len(fields) > 2 && fields[2] != "Z"
}

func fakeCodexCommand(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "codex")
	prefix := ""
	if mode == "grandchild" {
		pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
		t.Setenv(fakeGrandchildPIDEnv, pidFile)
		prefix = fmt.Sprintf("sleep 30 &\necho $! > '%s'\n", pidFile)
	}
	body := fmt.Sprintf("#!/bin/sh\n%sexec env %s=1 '%s' -test.run=TestFakeAppServer -- \"$@\"\n", prefix, fakeAppServerEnv, self)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERRSCHER_FAKE_MODE", mode)
	return script
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
	command := fakeCodexCommand(t, "grandchild")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := discover(ctx, contracts.DiscoveryAccess{Getenv: envWith(command)})
	if err == nil {
		t.Fatal("a hanging app-server listed models")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("discover took %v past its 300ms deadline", elapsed)
	}
	pidBytes, err := os.ReadFile(os.Getenv(fakeGrandchildPIDEnv))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if processAlive(t, pid) {
		t.Fatalf("grandchild %d still holds the app-server stdout after the deadline", pid)
	}
}

func TestDiscoverErrorCarriesBoundedStderr(t *testing.T) {
	command := fakeCodexCommand(t, "flood")
	_, err := discover(context.Background(), contracts.DiscoveryAccess{Getenv: envWith(command)})
	if err == nil || !strings.Contains(err.Error(), "no session token") {
		t.Fatalf("err = %v, want the app-server stderr in it", err)
	}
	if len(err.Error()) > discoverStderrMax+512 {
		t.Fatalf("error carries %d bytes, stderr is not bounded", len(err.Error()))
	}
}

func TestFakeAppServer(t *testing.T) {
	if os.Getenv(fakeAppServerEnv) != "1" {
		t.Skip("helper process only")
	}
	serveFakeAppServer(os.Stdin, os.Stdout, os.Getenv("HERRSCHER_FAKE_MODE"))
}

func reply(out *os.File, line string) {
	if _, err := out.WriteString(line + "\n"); err != nil {
		panic(err)
	}
}

func serveFakeAppServer(in, out *os.File, mode string) {
	if mode == "flood" {
		if _, err := os.Stderr.WriteString("no session token" + strings.Repeat("x", 100000)); err != nil {
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
