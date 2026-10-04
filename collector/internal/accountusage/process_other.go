//go:build !windows

package accountusage

import "os/exec"

func hideCommand(cmd *exec.Cmd) {}
