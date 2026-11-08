//go:build !unix

package docker

import "os/exec"

func detach(cmd *exec.Cmd) {}
