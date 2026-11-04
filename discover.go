package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	defaultDiscoverCmd   = "codex"
	discoverPageLimit    = 200
	discoverMaxPages     = 20
	discoverInitializeID = 0
	discoverModelListID  = 1
	discoverFrameMax     = 4 << 20
	discoverStderrMax    = 2048
	discoverWaitDelay    = 2 * time.Second
)

var (
	errCodexRPC       = errors.New("codex app-server refused the request")
	errModelListPages = errors.New("model/list paginates past the page limit")
	errFrameTooLarge  = errors.New("app-server frame exceeds the size limit")
)

type rpcFrame struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcFrameError  `json:"error"`
}

type rpcFrameError struct {
	Message string `json:"message"`
}

type modelListPage struct {
	Data       []codexModel `json:"data"`
	NextCursor *string      `json:"nextCursor"`
}

type codexModel struct {
	Model                    string            `json:"model"`
	DisplayName              string            `json:"displayName"`
	Hidden                   bool              `json:"hidden"`
	SupportedReasoningEffort []reasoningOption `json:"supportedReasoningEfforts"`
}

type reasoningOption struct {
	ReasoningEffort string `json:"reasoningEffort"`
}

func discover(ctx context.Context, access contracts.DiscoveryAccess) ([]contracts.ModelSpec, error) {
	command := strings.TrimSpace(access.Getenv("CODEX_CMD"))
	if command == "" {
		command = defaultDiscoverCmd
	}
	models, err := listAppServerModels(ctx, command)
	if err != nil {
		return nil, err
	}
	return specsFromCodex(models), nil
}

func listAppServerModels(ctx context.Context, command string) ([]codexModel, error) {
	cmd := exec.CommandContext(ctx, command, "app-server", "--listen", "stdio://")
	configureChild(cmd)
	cmd.WaitDelay = discoverWaitDelay
	stderr := &boundedBuffer{limit: discoverStderrMax}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open app-server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start app-server: %w", err)
	}
	models, sessionErr := runModelList(stdin, bufio.NewReader(stdout))
	if sessionErr != nil {
		if killErr := killProcessTree(cmd); killErr != nil {
			sessionErr = errors.Join(sessionErr, killErr)
		}
	}
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	switch {
	case sessionErr != nil:
		return nil, logAppServerFailure(sessionErr, stderr)
	case closeErr != nil:
		return nil, fmt.Errorf("close app-server stdin: %w", closeErr)
	case waitErr != nil:
		return nil, logAppServerFailure(fmt.Errorf("app-server exited: %w", waitErr), stderr)
	}
	return models, nil
}

func logAppServerFailure(err error, stderr *boundedBuffer) error {
	if text := loggableStderr(stderr.String()); text != "" {
		slog.Warn("app-server failed", "error", err, "stderr", text)
	}
	return err
}

func loggableStderr(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || looksLikeSecret(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, " | ")
}

func looksLikeSecret(line string) bool {
	lower := strings.ToLower(line)
	secretMarkers := []string{"token", "secret", "password", "passwd", "api_key", "apikey", "api-key", "authorization", "bearer", "credential", "cookie", "sk-"}
	for _, marker := range secretMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func runModelList(in io.Writer, out *bufio.Reader) ([]codexModel, error) {
	session := &rpcSession{in: in, out: out}
	if _, err := session.request(discoverInitializeID, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "herrscher", "version": "discovery"},
		"capabilities": make(map[string]any),
	}); err != nil {
		return nil, err
	}
	if err := session.notify("initialized"); err != nil {
		return nil, err
	}
	return listAllModels(session)
}

func listAllModels(session *rpcSession) ([]codexModel, error) {
	var models []codexModel
	cursor := ""
	for range discoverMaxPages {
		params := map[string]any{"includeHidden": false, "limit": discoverPageLimit}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := session.request(discoverModelListID, "model/list", params)
		if err != nil {
			return nil, err
		}
		var result modelListPage
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode model/list: %w", err)
		}
		models = append(models, result.Data...)
		if result.NextCursor == nil || *result.NextCursor == "" {
			return models, nil
		}
		cursor = *result.NextCursor
	}
	return nil, errModelListPages
}

func specsFromCodex(models []codexModel) []contracts.ModelSpec {
	var specs []contracts.ModelSpec
	seen := make(map[string]bool)
	for _, model := range models {
		if model.Hidden || model.Model == "" || seen[model.Model] {
			continue
		}
		seen[model.Model] = true
		specs = append(specs, contracts.ModelSpec{
			ID:      model.Model,
			Label:   model.DisplayName,
			Arg:     model.Model,
			Efforts: efforts(model.SupportedReasoningEffort),
			Route:   contracts.RouteNative,
		})
	}
	return specs
}

func efforts(options []reasoningOption) []string {
	var names []string
	for _, option := range options {
		if option.ReasoningEffort != "" {
			names = append(names, option.ReasoningEffort)
		}
	}
	return names
}

type rpcSession struct {
	in  io.Writer
	out *bufio.Reader
}

func (s *rpcSession) notify(method string) error {
	return s.write(map[string]any{"method": method})
}

func (s *rpcSession) request(requestID int, method string, params any) (json.RawMessage, error) {
	if err := s.write(map[string]any{"id": requestID, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		frame, err := s.readFrame()
		if err != nil {
			return nil, err
		}
		if frame.ID == nil || *frame.ID != requestID {
			continue
		}
		if frame.Error != nil {
			return nil, fmt.Errorf("%w: %s", errCodexRPC, frame.Error.Message)
		}
		return frame.Result, nil
	}
}

func (s *rpcSession) write(frame map[string]any) error {
	line, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("encode rpc frame: %w", err)
	}
	if _, err := s.in.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write rpc frame: %w", err)
	}
	return nil
}

func (s *rpcSession) readFrame() (rpcFrame, error) {
	line, err := readBoundedLine(s.out, discoverFrameMax)
	if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
		return rpcFrame{}, fmt.Errorf("read app-server: %w", err)
	}
	var frame rpcFrame
	if decodeErr := json.Unmarshal(line, &frame); decodeErr != nil {
		return rpcFrame{}, fmt.Errorf("decode app-server frame: %w", decodeErr)
	}
	return frame, nil
}

func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > limit {
			return nil, errFrameTooLarge
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}
