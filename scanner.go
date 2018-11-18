package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/micro/mdns"
)

type Host struct {
	Host string
	Addr string
}

type Scanner struct {
	host        string
	interactive bool
	stdin       io.Reader
	stdout      io.Writer
	ch          chan<- *Host
}

func NewScanner(host string, interactive bool, stdin io.Reader, stdout io.Writer, ch chan<- *Host) *Scanner {
	return &Scanner{
		host:        host,
		interactive: interactive,
		stdin:       stdin,
		stdout:      stdout,
		ch:          ch,
	}
}

func (s *Scanner) replyWithEntry(entry *mdns.ServiceEntry) {
	s.ch <- &Host{
		Host: entry.Host,
		Addr: entry.AddrV4.String() + ":" + strconv.Itoa(entry.Port),
	}
}

func (s *Scanner) printf(format string, args ...interface{}) {
	if s.interactive {
		fmt.Fprintf(s.stdout, format, args...)
	}
}

func (s *Scanner) askForEntry(entries []*mdns.ServiceEntry) {
	s.printf("found %d hosts\n", len(entries))
	for ii, v := range entries {
		s.printf("[%d]\t %s\n", ii+1, v.Host)
	}
	s.printf("select an entry [%d-%d]: ", 1, len(entries))
	r := bufio.NewReader(s.stdin)
	for {
		st, err := r.ReadString('\n')
		if err != nil {
			s.printf("error reading input: %v\n", err)
			continue
		}
		st = strings.TrimSpace(st)
		n, err := strconv.Atoi(st)
		if err != nil {
			s.printf("%q is not a valid number: %v\n", st, err)
			continue
		}
		if n < 1 || n > len(entries) {
			s.printf("%d is out of range [%d-%d]\n", n, 1, len(entries))
			continue
		}
		s.replyWithEntry(entries[n-1])
		break
	}
}

func (s *Scanner) Scan() {
	if s.host == "" {
		s.printf("scanning for hosts...\n")
	} else {
		s.printf("waiting for host %s...\n", s.host)
	}
	go func() {
		for {
			results := make(chan *mdns.ServiceEntry, 16)
			if err := mdns.Lookup("_esp32wmonitor._tcp", results); err != nil {
				panic(err)
			}
			close(results)
			var entries []*mdns.ServiceEntry
			for entry := range results {
				if s.host != "" && !strings.Contains(entry.Host, s.host) {
					continue
				}
				entries = append(entries, entry)
			}
			if len(entries) > 0 {
				if len(entries) > 1 {
					s.askForEntry(entries)
				} else {
					s.replyWithEntry(entries[0])
				}
				return
			}
			time.Sleep(time.Second)
		}
	}()
}
