//go:build awsderive && !windows

package derive

import (
	"os/exec"
	"syscall"
)

// See proc_windows.go for why the whole process tree has to be killed rather
// than just terraform itself: the provider plugins it spawns hold the state file
// open, and an orphaned plugin makes the next teardown fail for a reason
// unrelated to AWS.
//
// On Unix this is straightforward. Setpgid puts the child in its own process
// group, and a negative pid signals every member of that group.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Negative pid: the process group, not the process.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
