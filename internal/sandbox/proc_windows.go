//go:build windows

package sandbox

import "os/exec"

// prepareProcessGroup is a no-op on Windows; the default Kill is used.
func prepareProcessGroup(cmd *exec.Cmd) {}
