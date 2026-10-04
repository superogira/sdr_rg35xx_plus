//go:build linux

package radio

import (
	"os"
	"os/exec"
	"syscall"
)

// usbStartProcess launches rtl_tcp detached in its own session so it never
// receives our terminal signals; we kill it explicitly on the way out.
func usbStartProcess(path, logPath string) (*exec.Cmd, error) {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		log.Close()
		return nil, err
	}
	cmd := exec.Command(path, "-a", "127.0.0.1", "-p", "1234")
	cmd.Stdin = devNull
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		log.Close()
		devNull.Close()
		return nil, err
	}
	return cmd, nil
}
