//go:build linux

package extract

import (
	"os/exec"
	"syscall"
)

func setSandboxAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	// CLONE_NEWNET is not set — it requires CAP_SYS_ADMIN which is often
	// missing in containers, and canSandbox() returns false regardless.
	// All external parser execution requires trust_external opt-in.
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// canSandbox returns false — no real filesystem sandbox is implemented.
// All external parser execution requires trust_external.
func canSandbox() bool {
	return false
}
