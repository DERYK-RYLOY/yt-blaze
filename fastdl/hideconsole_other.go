//go:build !windows

package fastdl

import "os/exec"

// hideConsole is a no-op on non-Windows platforms.
func hideConsole(*exec.Cmd) {}
