package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"sync"
	"time"
)

const (
	cmdPrintStdout   = 0
	cmdPrintStderr   = 1
	cmdPong          = 5
	cmdOTAProgress   = 6
	cmdOTASuccess    = 7
	cmdOTAFailed     = 8
	cmdConfig        = 9
	cmdPing          = 128
	cmdReboot        = 129
	cmdOTA           = 130
	cmdContinue      = 131
	cmdCoredumpRead  = 132
	cmdCoredumpErase = 133
	cmdGetConfig     = 134
	cmdSetConfig     = 135
)

const (
	otaTimeout = time.Second * 5 // Timeout between messages
)

const (
	hostConfigVersion     = 1
	hostConfigSSIDLen     = 33
	hostConfigPasswordLen = 64
)

const (
	WifiModeAuto = iota
	WifiModeSTA
	WifiModeAP
)

type HostConfig struct {
	Version      uint8
	WifiSSID     string
	WifiPassword string
	WifiMode     uint8
}

type Client struct {
	Host   *Host
	info   *ProjectInfo
	conn   net.Conn
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	mu             sync.Mutex
	timeouts       int
	otaSize        int
	otaLastMessage time.Time

	onConfig func(*HostConfig)
}

func NewClient(info *ProjectInfo, stdin io.Reader, stdout io.Writer, stderr io.Writer) *Client {
	return &Client{
		info:   info,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
	}
}

func (c *Client) ProjectInfo() *ProjectInfo {
	return c.info
}

func (c *Client) Stdin() io.Reader {
	return c.stdin
}

func (c *Client) Stdout() io.Writer {
	return c.stdout
}

func (c *Client) Connect() error {
	conn, err := net.Dial("tcp", c.Host.Addr)
	if err != nil {
		return fmt.Errorf("error connecting to %s: %v", c.Host.Host, err)
	}
	c.conn = conn
	c.timeouts = 0
	c.otaSize = 0
	// First, try to find a coredump so we can retrieve it
	// before the host crashes again
	c.writeByte(cmdCoredumpRead)
	return nil
}

func (c *Client) Close() error {
	if c.conn != nil {
		conn := c.conn
		// Set to nil here, so Run() can detect that we
		// closed the connection intentionally
		c.conn = nil
		if err := conn.Close(); err != nil {
			c.conn = conn
			return err
		}
	}
	return nil
}

func (c *Client) isFlashingOTA() bool {
	return c.otaSize > 0 && time.Since(c.otaLastMessage) < otaTimeout
}

func (c *Client) write(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var err error
	conn := c.conn
	if conn != nil {
		// Don't write more than 100K/s, otherwise the ESP32
		// might drop packets
		blocksize := 100 * 1024
		interval := 1000 * time.Millisecond
		for pos := 0; pos < len(data); pos += blocksize {
			end := pos + blocksize
			if end > len(data) {
				end = len(data)
			}
			if _, err = conn.Write(data[pos:end]); err != nil {
				return err
			}
			if pos < len(data) {
				time.Sleep(interval)
			}

		}
	} else {
		err = errors.New("not connected")
	}
	return err
}

func (c *Client) writeByte(b byte) error {
	return c.write([]byte{b})
}

func (c *Client) print(conn net.Conn, w io.Writer) error {
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var s uint32
	if err := binary.Read(conn, binary.BigEndian, &s); err != nil {
		return err
	}
	data := make([]byte, int(s))
	if _, err := io.ReadFull(conn, data); err != nil {
		return err
	}
	if w != nil && !c.isFlashingOTA() {
		fmt.Fprint(w, string(data))
	}
	return nil
}

func (c *Client) readBlob(conn net.Conn, size interface{}) ([]byte, error) {
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if err := binary.Read(conn, binary.BigEndian, size); !c.handleError(err) {
		return nil, err
	}
	var blobSize int
	switch x := size.(type) {
	case *uint16:
		blobSize = int(*x)
	case *uint32:
		blobSize = int(*x)
	default:
		panic(fmt.Errorf("invalid blob length type %T", size))
	}
	data := make([]byte, blobSize)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(conn, data); !c.handleError(err) {
		return nil, err
	}
	return data, nil
}

func (c *Client) readBlob32(conn net.Conn) ([]byte, error) {
	var size uint32
	return c.readBlob(conn, &size)
}

func (c *Client) readBlob16(conn net.Conn) ([]byte, error) {
	var size uint16
	return c.readBlob(conn, &size)
}

func (c *Client) handleError(err error) bool {
	if err != nil {
		if nerr, ok := err.(*net.OpError); ok {
			if nerr.Timeout() && c.timeouts == 0 {
				if c.isFlashingOTA() {
					return true
				}
				c.timeouts++
				c.writeByte(cmdPing)
				return true
			}
		}
		return false
	}
	return true
}

func (c *Client) GetConfig(f func(*HostConfig)) error {
	c.onConfig = f
	return c.writeByte(cmdGetConfig)
}

func (c *Client) SetConfig(cfg *HostConfig) error {
	var buf bytes.Buffer
	buf.WriteByte(cmdSetConfig)
	size := uint16(hostConfigSSIDLen + hostConfigPasswordLen + 2)
	if err := binary.Write(&buf, binary.BigEndian, size); err != nil {
		return err
	}
	buf.WriteByte(hostConfigVersion)
	ssidData := []byte(cfg.WifiSSID)
	for len(ssidData) < hostConfigSSIDLen {
		ssidData = append(ssidData, 0)
	}
	buf.Write(ssidData)
	passwordData := []byte(cfg.WifiPassword)
	for len(passwordData) < hostConfigPasswordLen {
		passwordData = append(passwordData, 0)
	}
	buf.Write(passwordData)
	buf.WriteByte(byte(cfg.WifiMode))
	return c.write(buf.Bytes())
}

func (c *Client) Run() error {
	cmd := make([]byte, 1)
	for {
		conn := c.conn
		if conn == nil {
			// Closed() was called
			break
		}
		// We want to fail fast here, so we can reconnect quickly
		// if the host crashes
		conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, err := conn.Read(cmd)
		if err != nil {
			if c.conn == nil {
				// Closed intentionally
				break
			}
			if c.handleError(err) {
				continue
			}
			return err
		}
		c.timeouts = 0
		switch cmd[0] {
		case cmdPrintStdout:
			if err := c.print(conn, c.stdout); !c.handleError(err) {
				return err
			}
		case cmdPrintStderr:
			if err := c.print(conn, c.stderr); !c.handleError(err) {
				return err
			}
		case cmdPong:
			// Nothing do to
		case cmdOTAProgress:
			if !c.isFlashingOTA() {
				break
			}
			var offset uint32
			conn.SetReadDeadline(time.Now().Add(time.Second))
			if err := binary.Read(conn, binary.BigEndian, &offset); !c.handleError(err) {
				return err
			}
			percentage := int(offset) * 100 / c.otaSize
			fmt.Fprintf(c.stdout, "OTA progress (%v/%v) (%d%%)\r", offset, c.otaSize, percentage)
			c.otaLastMessage = time.Now()
		case cmdOTAFailed:
			if !c.isFlashingOTA() {
				break
			}
			fmt.Fprintf(c.stdout, "OTA failed\n")
			c.otaSize = 0
		case cmdOTASuccess:
			if !c.isFlashingOTA() {
				break
			}
			fmt.Fprintf(c.stdout, "OTA finished\n")
			c.otaSize = 0
		case cmdContinue:
			fmt.Fprintf(c.stdout, "host was awaiting for us and has now continued...\n")
		case cmdCoredumpRead:
			data, err := c.readBlob32(conn)
			if err != nil {
				return err
			}
			if len(data) == 0 {
				// No coredump, just continue
				c.writeByte(cmdContinue)
				break
			}
			fmt.Fprintf(c.stdout, "Found a coredump of %v bytes, retrieving...\n", len(data))
			del, err := c.DisplayCoreDump(data)
			if err != nil {
				fmt.Fprintf(c.stderr, "Error displaying coredump: %v\n", err)
			}
			if del {
				c.writeByte(cmdCoredumpErase)
			} else {
				c.writeByte(cmdContinue)
			}
		case cmdCoredumpErase:
			// Coredump is now erased, instruct the host to continue
			c.writeByte(cmdContinue)
		case cmdConfig:
			var size uint16
			conn.SetReadDeadline(time.Now().Add(time.Second))
			if err := binary.Read(conn, binary.BigEndian, &size); !c.handleError(err) {
				return err
			}
			var cfg HostConfig
			if size > 0 {
				if err := binary.Read(conn, binary.BigEndian, &cfg.Version); !c.handleError(err) {
					return err
				}

				ssidData := make([]byte, hostConfigSSIDLen)
				if _, err := io.ReadFull(conn, ssidData); !c.handleError(err) {
					return err
				}
				cfg.WifiSSID = string(bytes.Trim(ssidData, "\x00"))

				passwordData := make([]byte, hostConfigPasswordLen)
				if _, err := io.ReadFull(conn, passwordData); !c.handleError(err) {
					return err
				}
				cfg.WifiPassword = string(bytes.Trim(passwordData, "\x00"))

				if err := binary.Read(conn, binary.BigEndian, &cfg.WifiMode); !c.handleError(err) {
					return err
				}
			}
			onConfig := c.onConfig
			if onConfig != nil {
				onConfig(&cfg)
			} else {
				// We're probably getting it as a response to setting the config, apply it
				c.Reboot()
			}
			c.onConfig = nil
		default:
			fmt.Fprintf(c.stderr, "unknown command %v\n", cmd[0])
		}
	}
	return nil
}

func (c *Client) Reboot() error {
	fmt.Fprintf(c.stdout, "rebooting %s...\n", c.Host.Host)
	return c.writeByte(cmdReboot)
}

func (c *Client) Flash(bin string) error {
	data, err := ioutil.ReadFile(bin)
	if err != nil {
		return err
	}
	c.otaSize = len(data)
	c.otaLastMessage = time.Now()
	var buf bytes.Buffer
	buf.WriteByte(cmdOTA)
	binary.Write(&buf, binary.BigEndian, uint32(len(data)))
	buf.Write(data)
	return c.write(buf.Bytes())
}
