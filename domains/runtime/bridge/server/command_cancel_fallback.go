//go:build !unix

package server

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func configureCommandCancellation(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	configurePTYCommandCancellation(cmd)
}

func configurePTYCommandCancellation(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.Cancel = func() error {
		return killProcess(cmd)
	}
	cmd.WaitDelay = 2 * time.Second
}

func killProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
