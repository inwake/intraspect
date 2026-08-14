//go:build !windows

package shell

import "os/exec"

func hideWindow(cmd *exec.Cmd) {
}
