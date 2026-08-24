//go:build !unix

package sandbox

import (
	"errors"
	"os/exec"
)

func setProcessGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func asExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}
