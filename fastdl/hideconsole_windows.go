//go:build windows

package fastdl

import (
	"os/exec"
	"syscall"
)

// hideConsole marks cmd so its console window is never shown. Every console
// subprocess (aria2c, yt-dlp, ffmpeg) flashes a black box on the desktop's
// GUI app unless CREATE_NO_WINDOW is passed at creation time.
func hideConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		HideWindow:    true,
	}
}
