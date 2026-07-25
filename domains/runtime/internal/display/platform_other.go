//go:build !windows

package display

import "os/exec"

func hideCommandWindow(_ *exec.Cmd) {}
