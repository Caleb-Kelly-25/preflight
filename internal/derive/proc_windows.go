//go:build awsderive && windows

package derive

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
)

// Terraform spawns provider plugins as child processes, and those children hold
// the state file open. Killing only the parent leaves them running: the next
// `terraform destroy` then fails with "the process cannot access the file
// because another process has locked a portion of the file", which looks like a
// teardown failure and aborts the derivation for a reason that has nothing to do
// with AWS.
//
// That is not hypothetical. It is what happened deriving aws_s3_bucket: removing
// s3:ListBucket makes the provider retry HeadBucket until the budget expires,
// the apply is killed, and the orphaned plugin then blocked teardown. The run
// was discarded and a bucket leaked.
//
// CREATE_NEW_PROCESS_GROUP puts terraform and its children in one group so the
// whole tree can be addressed at once.
const createNewProcessGroup = 0x00000200

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// killProcessTree kills terraform and every process it started.
//
// taskkill /T walks the tree; /F is required because a plugin mid-request does
// not respond to a polite request. Windows has no portable equivalent of
// signalling a process group from Go, so shelling out to the tool built for it
// is the honest option — and taskkill ships with every Windows install, so this
// adds no dependency.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if out, err := exec.Command("taskkill", "/T", "/F", "/PID", pid).CombinedOutput(); err != nil {
		// Fall back to killing the parent alone. Worse — it can orphan a plugin —
		// but better than leaving a hung apply running to the end of time.
		if kerr := cmd.Process.Kill(); kerr != nil {
			return fmt.Errorf("taskkill failed (%v: %s) and killing the process failed: %w", err, out, kerr)
		}
	}
	return nil
}
