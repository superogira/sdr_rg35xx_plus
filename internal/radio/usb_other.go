// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package radio

import (
	"os"
	"os/exec"
)

// usbStartProcess is the portable fallback (Windows dev builds): plain
// spawn, no session games.
func usbStartProcess(path, logPath string) (*exec.Cmd, error) {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, "-a", "127.0.0.1", "-p", "1234")
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	return cmd, nil
}
