// Package rtltcp speaks the rtl_tcp wire protocol: a 52-byte dongle-info
// handshake on connect, 5-byte little-endian commands from the client, and
// an endless stream of unsigned 8-bit I/Q pairs from the server.
package rtltcp

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Command opcodes (rtl-sdr src/rtl_tcp.c).
const (
	CmdSetFrequency      = 0x01
	CmdSetSampleRate     = 0x02
	CmdSetGainMode       = 0x03 // 0 = manual, 1 = tuner AGC
	CmdSetGain           = 0x04 // gain in tenths of dB (manual mode)
	CmdSetFreqCorrection = 0x05
	CmdSetDirectSampling = 0x09 // 0 = off, 1 = I branch, 2 = Q branch (HF)
	CmdSetAGCMode        = 0x08 // RTL2832 IF AGC
	CmdSetGainByIndex    = 0x0d
)

// DongleInfo is the 52-byte header the server sends right after accept.
type DongleInfo struct {
	Magic     string // "RTL0"
	TunerType int32
	GainCount int32
	Extra     [10]int32
}

// Client is one rtl_tcp connection. It is safe for one writer goroutine to
// call the command setters while another goroutine reads the IQ stream.
type Client struct {
	conn net.Conn
	mu   sync.Mutex // serializes command writes

	// bigEndian selects the byte order of command parameters. The
	// e25wop server is big-endian end to end (handshake AND commands —
	// confirmed against the user's rtl-sdr-web-monitor, which packs
	// commands with struct ">BI"); standard rtl_tcp is little-endian.
	// The choice follows the handshake sniff below.
	bigEndian bool

	// poisoned marks a connection whose command stream can no longer be
	// trusted: a partial command write (deadline hit mid-frame) shifts the
	// server's parser, turning every later command into garbage. Once
	// poisoned the only safe move is reconnect.
	poisoned bool

	Info DongleInfo
}

// Dial connects and completes the dongle-info handshake.
func Dial(address string, timeout time.Duration) (*Client, error) {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(timeout))
	c := &Client{conn: conn}
	if err := c.handshake(); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return c, nil
}

func (c *Client) handshake() error {
	var buf [52]byte
	if _, err := io.ReadFull(c.conn, buf[:]); err != nil {
		return fmt.Errorf("handshake read: %w", err)
	}
	c.Info.Magic = string(buf[0:4])
	if c.Info.Magic != "RTL0" {
		return fmt.Errorf("bad magic %q — not an rtl_tcp server?", c.Info.Magic)
	}
	c.Info.TunerType = int32(binary.LittleEndian.Uint32(buf[4:8]))
	c.Info.GainCount = int32(binary.LittleEndian.Uint32(buf[8:12]))
	// Some server builds (ESP32 bridges and the e25wop server) send the
	// header big-endian; detect the nonsense range and flip. Tuner types
	// are a small enum and gain counts stay under ~64.
	if c.Info.TunerType > 0xFF {
		c.Info.TunerType = int32(binary.BigEndian.Uint32(buf[4:8]))
		c.Info.GainCount = int32(binary.BigEndian.Uint32(buf[8:12]))
		// The whole protocol of such builds is network byte order:
		// command parameters too.
		c.bigEndian = true
	}
	return nil
}

// send writes one 5-byte command. A short write poisons the connection:
// the server's parser would be byte-shifted forever, so the caller must
// reconnect.
func (c *Client) send(cmd byte, param uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned {
		return fmt.Errorf("connection poisoned (earlier short write)")
	}
	var b [5]byte
	b[0] = cmd
	if c.bigEndian {
		binary.BigEndian.PutUint32(b[1:5], param)
	} else {
		binary.LittleEndian.PutUint32(b[1:5], param)
	}
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	n, err := c.conn.Write(b[:])
	c.conn.SetWriteDeadline(time.Time{})
	if err != nil || n != len(b) {
		c.poisoned = true
		return fmt.Errorf("cmd 0x%02x param %d: wrote %d/5 bytes: %v — connection reset required", cmd, param, n, err)
	}
	return nil
}

func (c *Client) SetFrequency(hz uint32) error { return c.send(CmdSetFrequency, hz) }
func (c *Client) SetSampleRate(hz uint32) error {
	return c.send(CmdSetSampleRate, hz)
}
func (c *Client) SetTunerAGC(on bool) error {
	v := uint32(0)
	if on {
		v = 1
	}
	return c.send(CmdSetGainMode, v)
}
func (c *Client) SetRTLAGC(on bool) error {
	v := uint32(0)
	if on {
		v = 1
	}
	return c.send(CmdSetAGCMode, v)
}
func (c *Client) SetGainTenthsDB(tenths int32) error {
	return c.send(CmdSetGain, uint32(tenths))
}
func (c *Client) SetGainByIndex(idx int) error {
	return c.send(CmdSetGainByIndex, uint32(idx))
}

// SetDirectSampling switches HF direct sampling: 0 = tuner (VHF/UHF),
// 1 = I branch, 2 = Q branch (the RTL-SDR Blog V3/V4 HF path).
func (c *Client) SetDirectSampling(mode int) error {
	return c.send(CmdSetDirectSampling, uint32(mode))
}

// ReadIQ fills buf with the raw interleaved-uint8 IQ byte stream. It blocks
// until the buffer is full, the connection breaks, or the deadline passes.
func (c *Client) ReadIQ(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	return c.conn.Read(buf)
}

// SetReadDeadline bounds the wait for the next IQ bytes: a stalled server
// (Wi-Fi drop that has not torn down TCP yet) must trip a reconnect instead
// of freezing the audio silently.
func (c *Client) SetReadDeadline(t time.Time) { c.conn.SetReadDeadline(t) }

// ReadIQFull blocks until buf is completely filled with IQ bytes.
func (c *Client) ReadIQFull(buf []byte) error {
	_, err := io.ReadFull(c.conn, buf)
	return err
}

func (c *Client) Close() error { return c.conn.Close() }
