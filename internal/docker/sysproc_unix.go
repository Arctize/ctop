//go:build unix

package docker

import (
	"os/exec"
	"syscall"
)

// detach starts the command in its own session so terminal-generated
// signals (ctrl-c while attached, ctrl-z, …) don't reach it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
