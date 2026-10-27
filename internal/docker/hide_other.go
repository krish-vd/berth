//go:build !windows

package docker

import "os/exec"

func hideWindow(*exec.Cmd) {}
