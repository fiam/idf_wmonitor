package main

import (
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/pkg/term"
	"github.com/pkg/term/termios"
)

const (
	kmArrowLeft  = 252
	kmArrowRight = 253
	kmArrowDown  = 254
	kmArrowUp    = 255
)

var (
	kmSigInt = byte(3) // ctrl+c
)

type keyboardMonitor struct {
	t     *term.Term
	isRaw bool
	mu    sync.Mutex
}

func (km *keyboardMonitor) Open() error {
	km.mu.Lock()
	defer km.mu.Unlock()
	if km.t == nil {
		t, err := term.Open("/dev/tty")
		if err != nil {
			return err
		}
		km.t = t
	}
	if err := km.t.SetRaw(); err != nil {
		return err
	}
	km.isRaw = true
	return nil
}

func (km *keyboardMonitor) Get() (byte, error) {
	km.mu.Lock()
	t := km.t
	isRaw := km.isRaw
	km.mu.Unlock()
	if t != nil && isRaw {
		buf := make([]byte, 3)
		// We can't use t.SetReadTimeout() because zero
		// disables timeouts
		var tios syscall.Termios
		if err := termios.Tcgetattr(0, &tios); err != nil {
			panic(err)
		}
		tios.Cc[syscall.VMIN], tios.Cc[syscall.VTIME] = 0, 0
		if err := termios.Tcsetattr(0, termios.TCSANOW, &tios); err != nil {
			panic(err)
		}
		n, err := t.Read(buf)
		if err != nil {
			if err == io.EOF {
				return 0, nil
			}
			return 0, err
		}
		if n == 3 && buf[0] == 27 && buf[1] == 91 {
			// Arrow key
			return 255 - (buf[2] - 65), nil
		}
		return buf[0], nil
	}
	return 0, nil
}

func (km *keyboardMonitor) Close() error {
	km.mu.Lock()
	defer km.mu.Unlock()
	if km.t != nil {
		if err := km.t.Restore(); err != nil {
			return err
		}
		km.isRaw = false
	}
	return nil
}

func (km *keyboardMonitor) RunPaused(fn func()) {
	wasOpen := km.t != nil
	if wasOpen {
		if err := km.Close(); err != nil {
			panic(err)
		}
	}
	fn()
	if wasOpen {
		if err := km.Open(); err != nil {
			panic(err)
		}
	}
}

type keyboardMonitorWriter struct {
	km *keyboardMonitor
	w  io.Writer
}

func (w *keyboardMonitorWriter) Write(p []byte) (n int, err error) {
	w.km.RunPaused(func() {
		n, err = w.w.Write(p)
	})
	return n, err
}

func (km *keyboardMonitor) Stdout() io.Writer {
	return &keyboardMonitorWriter{
		km: km,
		w:  os.Stdout,
	}
}

func (km *keyboardMonitor) Stderr() io.Writer {
	return &keyboardMonitorWriter{
		km: km,
		w:  os.Stderr,
	}
}

type keyboardMonitorReader struct {
	km *keyboardMonitor
	r  io.Reader
}

func (r *keyboardMonitorReader) Read(data []byte) (n int, err error) {
	r.km.RunPaused(func() {
		n, err = r.r.Read(data)
	})
	return n, err
}

func (km *keyboardMonitor) Stdin() io.Reader {
	return &keyboardMonitorReader{
		km: km,
		r:  os.Stdin,
	}
}

func (km *keyboardMonitor) Start() <-chan byte {
	input := make(chan byte)
	go func() {
		for {
			k, err := km.Get()
			if err == nil {
				input <- k
			}
		}
	}()
	return input
}
