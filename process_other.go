//go:build !unix

package codex

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

func configureChild(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return killProcessTree(cmd)
	}
}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run() == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
