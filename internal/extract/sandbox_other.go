//go:build !linux

package extract

import "os/exec"

func setSandboxAttrs(cmd *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}

// canSandbox returns false on non-Linux platforms (no enforced isolation).
func canSandbox() bool {
	return false
}
