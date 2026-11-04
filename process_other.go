//go:build !unix

package codex

import (
	"errors"
	"os"
	"os/exec"
)

func configureChild(*exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) error {
	err := cmd.Process.Kill()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
