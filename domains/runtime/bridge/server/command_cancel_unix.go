//go:build unix

package server

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureCommandCancellation(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	configureProcessGroupCancel(cmd)
}

func configurePTYCommandCancellation(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	// creack/pty sets SysProcAttr.Setsid/Setctty before start. Setting
	// Setpgid here makes that session setup fail with EPERM on Linux.
	configureProcessGroupCancel(cmd)
}

func configureProcessGroupCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		pgid, err := syscall.Getpgid(cmd.Process.Pid)
		if err == nil {
			if killErr := syscall.Kill(-pgid, syscall.SIGKILL); killErr != nil && killErr != syscall.ESRCH {
				return killErr
			}
			return nil
		}
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
