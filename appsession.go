package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type turnResult struct {
	Text      string
	ThreadID  string
	IsError   bool
	ErrMsg    string
	rejected  bool
	completed bool
}

type turnOutcome struct {
	turn turnResult
	err  error
}

type appSession struct {
	mu                 sync.Mutex
	stdin              io.WriteCloser
	out                *bufio.Reader
	outClose           io.Closer
	cmd                *exec.Cmd
	threadID           string
	model, effort, dir string
	nextID             int
	approvalsIfGated   *approver
	closeOnce          sync.Once
	closeErr           error
}

func newAppSession(stdin io.WriteCloser, out io.Reader) *appSession {
	session := &appSession{stdin: stdin, out: bufio.NewReader(out), nextID: 1}
	if c, ok := out.(io.Closer); ok {
		session.outClose = c
	}
	return session
}

func initializeRequest(id int) map[string]any {
	return map[string]any{fieldID: id, fieldMethod: "initialize", fieldParams: map[string]any{"clientInfo": map[string]any{"name": "herrscher_codex_backend", "title": "Herrscher Codex Backend", "version": "0.1.0"}}}
}

func threadStartRequest(requestID int, model, dir, resume string) map[string]any {
	params := make(map[string]any)
	if model != "" {
		params["model"] = model
	}
	if dir != "" {
		params["cwd"] = dir
	}
	if resume != "" {
		return map[string]any{fieldID: requestID, fieldMethod: "thread/resume", fieldParams: map[string]any{fieldThreadID: resume}}
	}
	return map[string]any{fieldID: requestID, fieldMethod: "thread/start", fieldParams: params}
}

func turnStartRequest(requestID int, thread, text, model, effort string) map[string]any {
	params := map[string]any{fieldThreadID: thread, fieldInput: []any{map[string]any{fieldType: contentText, fieldText: text}}}
	if model != "" {
		params["model"] = model
	}
	if effort != "" {
		params["effort"] = effort
	}
	return map[string]any{fieldID: requestID, fieldMethod: "turn/start", fieldParams: params}
}

func (s *appSession) write(msg map[string]any) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	_, err = s.stdin.Write(payload)
	return err
}

func (s *appSession) waitResponse(requestID int) (map[string]any, error) {
	for {
		line, err := s.out.ReadBytes('\n')
		if len(line) > 0 {
			var msg map[string]any
			if json.Unmarshal(line, &msg) == nil {
				if n, ok := msg[fieldID].(float64); ok && int(n) == requestID {
					return msg, nil
				}
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

func (s *appSession) initialize(resume string) error {
	if err := s.write(initializeRequest(s.nextID)); err != nil {
		return err
	}
	requestID := s.nextID
	s.nextID++
	if _, err := s.waitResponse(requestID); err != nil {
		return err
	}
	if err := s.write(map[string]any{fieldMethod: "initialized", fieldParams: make(map[string]any)}); err != nil {
		return err
	}
	requestID = s.nextID
	s.nextID++
	if err := s.write(threadStartRequest(requestID, s.model, s.dir, resume)); err != nil {
		return err
	}
	msg, err := s.waitResponse(requestID)
	if err != nil {
		return err
	}
	result := mapField(msg, fieldResult)
	thread := mapField(result, fieldThread)
	s.threadID = stringField(thread, fieldID)
	if s.threadID == "" {
		s.threadID = resume
	}
	if s.threadID == "" {
		return errNoThreadID
	}
	return nil
}

func startAppSession(lifetimeCtx, turnCtx context.Context, base []string, model, effort, dir string, verbose bool, resume string, env map[string]string, approvals *approver) (*appSession, error) {
	approvals = approvalsForTurn(turnCtx, approvals)
	argv := appServerArgv(base, approvals != nil)
	cmd := exec.CommandContext(lifetimeCtx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = contracts.MergeEnv(os.Environ(), env)
	if verbose {
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stderr = io.Discard
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	session := newAppSession(stdin, out)
	session.cmd = cmd
	session.model = model
	session.effort = effort
	session.dir = dir
	session.approvalsIfGated = approvals
	if err := session.initializeWithinTurn(turnCtx, resume); err != nil {
		if closeErr := session.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	return session, nil
}

func (s *appSession) initializeWithinTurn(ctx context.Context, resume string) error {
	done := make(chan error, 1)
	go func() { done <- s.initialize(resume) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		s.abortAndJoinHandshake(done)
		return ctx.Err()
	}
}

func (s *appSession) abortAndJoinHandshake(done <-chan error) {
	s.abort()
	if initErr := <-done; initErr != nil {
		slog.Debug("initialize ended with the aborted session", "error", initErr)
	}
}

func (s *appSession) Send(ctx context.Context, text string, onEvent func(contracts.BackendEvent)) (turnResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-ctx.Done():
		return turnResult{}, ctx.Err()
	default:
	}
	requestID := s.nextID
	s.nextID++
	if err := s.write(turnStartRequest(requestID, s.threadID, text, s.model, s.effort)); err != nil {
		return turnResult{}, errors.Join(errTurnNotStarted, err)
	}
	return s.awaitTurn(ctx, turnScope{threadID: s.threadID, requestID: requestID}, onEvent)
}

func (s *appSession) awaitTurn(ctx context.Context, scope turnScope, onEvent func(contracts.BackendEvent)) (turnResult, error) {
	result := make(chan turnOutcome, 1)
	go func() {
		tr, err := readTurn(ctx, s.out, scope, onEvent, s.approvalsIfGated, s.write)
		result <- turnOutcome{turn: tr, err: err}
	}()
	select {
	case done := <-result:
		return done.turn, done.err
	case <-ctx.Done():
		s.abort()
		<-result
		return turnResult{}, ctx.Err()
	}
}

func (s *appSession) Close() error {
	s.abort()
	return s.closeErr
}

func (s *appSession) abort() {
	s.closeOnce.Do(func() { s.closeErr = s.release() })
}

func (s *appSession) release() error {
	var errs []error
	if s.stdin != nil {
		errs = append(errs, s.stdin.Close())
	}
	if s.outClose != nil {
		errs = append(errs, s.outClose.Close())
	}
	if s.cmd != nil && s.cmd.Process != nil {
		errs = append(errs, s.stopProcess())
	}
	return errors.Join(errs...)
}

func (s *appSession) stopProcess() error {
	killErr := s.cmd.Process.Kill()
	waitErr := s.cmd.Wait()
	if _, exited := errors.AsType[*exec.ExitError](waitErr); exited && killErr == nil {
		waitErr = nil
	}
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	return errors.Join(killErr, waitErr)
}
