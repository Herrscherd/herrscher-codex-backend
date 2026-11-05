//go:build !unix

package codex

import (
	"context"
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
	treeErr := exec.CommandContext(context.Background(), "taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	if treeErr == nil {
		return nil
	}
	return killAfterTreeFailure(cmd.Process, treeErr)
}
