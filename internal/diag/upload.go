package diag

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

type FTPConfig struct {
	Host string
	User string
	Pass string
}

func UploadLog(cfg FTPConfig, localPath, remoteName string) error {
	conn, err := net.DialTimeout("tcp", cfg.Host, 10*time.Second)
	if err != nil {
		return fmt.Errorf("ftp connect: %w", err)
	}
	defer conn.Close()

	readResp := func(wantCode string) error {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 512)
		n, _ := conn.Read(buf)
		if n == 0 {
			return fmt.Errorf("ftp: empty response")
		}
		resp := string(buf[:n])
		if !strings.HasPrefix(resp, wantCode) {
			return fmt.Errorf("ftp resp %.3s want %s", resp, wantCode)
		}
		return nil
	}
	sendCmd := func(cmd string) error {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err := conn.Write([]byte(cmd + "\r\n"))
		return err
	}

	if err := readResp("220"); err != nil {
		return err
	}
	_ = sendCmd("USER " + cfg.User)
	_ = readResp("331")
	_ = sendCmd("PASS " + cfg.Pass)
	if err := readResp("230"); err != nil {
		return err
	}
	_ = sendCmd("TYPE I")
	_ = readResp("200")

	_ = sendCmd("PASV")
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	resp := string(buf[:n])
	if !strings.HasPrefix(resp, "227") {
		return fmt.Errorf("ftp PASV: %.30s", resp)
	}
	inner := resp
	if i := strings.Index(inner, "("); i >= 0 {
		if j := strings.LastIndex(inner, ")"); j > i {
			inner = inner[i+1 : j]
		}
	}
	parts := strings.Split(inner, ",")
	if len(parts) != 6 {
		return fmt.Errorf("ftp PASV parse: %s", inner)
	}
	dataHost := fmt.Sprintf("%s.%s.%s.%s", parts[0], parts[1], parts[2], parts[3])
	dataPort := (atoi(parts[4]) << 8) | atoi(parts[5])

	dataConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", dataHost, dataPort), 10*time.Second)
	if err != nil {
		return fmt.Errorf("ftp data: %w", err)
	}

	_ = sendCmd("STOR " + remoteName)
	_ = readResp("150")

	data, err := os.ReadFile(localPath)
	if err != nil {
		dataConn.Close()
		return err
	}
	dataConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, werr := dataConn.Write(data)
	dataConn.Close()
	if werr != nil {
		return fmt.Errorf("ftp write: %w", werr)
	}

	_ = sendCmd("QUIT")
	return nil
}

func atoi(s string) int {
	v := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			break
		}
		v = v*10 + int(c-'0')
	}
	return v
}
