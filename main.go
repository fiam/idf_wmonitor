package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	projectPathArg    = flag.String("p", ".", "Path to the project directory")
	hostArg           = flag.String("host", "", "Host to connect to, leave empty for scanning")
	nonInteractiveArg = flag.Bool("n", false, "Non interactive")
	makefiles         = flag.String("m", "Makefile", "Name of the Makefile to use to load the app information (relative to project directory)")
)

type ProjectInfo struct {
	Path    string
	IDFPath string
	AppElf  string
	AppBin  string
}

func handleInput(km *keyboardMonitor, ch chan<- byte) {
	for {
		b, err := km.Get()
		if err != nil {
			panic(err)
		}
		ch <- b
	}
}

func findProjectInfo(projectPath string) (*ProjectInfo, error) {
	var makefilePaths []string
	for _, v := range strings.Split(*makefiles, ",") {
		makefilePaths = append(makefilePaths, filepath.Join(projectPath, v))
	}
	varNames := []string{"IDF_PATH", "APP_ELF", "APP_BIN"}
	vars, err := ResolveMakefileVariables(makefilePaths, varNames...)
	if err != nil {
		return nil, err
	}
	for _, v := range varNames {
		if vars[v] == "" {
			return nil, fmt.Errorf("could not resolve variable $%s", v)
		}
	}
	return &ProjectInfo{
		Path:    projectPath,
		IDFPath: vars["IDF_PATH"],
		AppElf:  vars["APP_ELF"],
		AppBin:  vars["APP_BIN"],
	}, nil
}

func handleServer(hostFilter string, interactive bool, c *Client, ch chan<- error) {
	hostCh := make(chan *Host, 1)
	s := NewScanner(hostFilter, interactive, c.Stdin(), c.Stdout(), hostCh)
	s.Scan()
	host := <-hostCh

	c.Host = host
	if err := c.Connect(); err != nil {
		ch <- err
		return
	}
	fmt.Fprintf(c.Stdout(), "connected to %s\n", host.Host)
	defer c.Close()

	err := c.Run()
	ch <- err
}

func flash(km *keyboardMonitor, c *Client) error {
	// Compile
	info := c.ProjectInfo()
	compileCmd := exec.Command("make", info.AppBin)
	compileCmd.Dir = info.Path
	compileCmd.Stdout = km.Stdout()
	compileCmd.Stderr = km.Stderr()
	if err := compileCmd.Run(); err != nil {
		return errors.New("compilation failed")
	}
	return c.Flash(info.AppBin)
}

func main() {
	flag.Parse()

	info, err := findProjectInfo(*projectPathArg)
	if err != nil {
		panic(err)
	}

	km := &keyboardMonitor{}
	inputCh := make(chan byte, 1)

	var stdin io.Reader = os.Stdin
	var stdout io.Writer = os.Stdout
	var stderr io.Writer = os.Stderr

	if !*nonInteractiveArg {
		if err := km.Open(); err != nil {
			panic(err)
		}
		defer km.Close()
		go handleInput(km, inputCh)
		stdin = km.Stdin()
		stdout = km.Stdout()
		stderr = km.Stderr()
	}

	clientCh := make(chan error, 1)

	hostFilter := *hostArg
	c := NewClient(info, stdin, stdout, stderr)
	for {
		go handleServer(hostFilter, !*nonInteractiveArg, c, clientCh)
	PollingLoop:
		for {
			select {
			case input := <-inputCh:
				switch input {
				case kmSigInt:
					km.Close()
					syscall.Kill(syscall.Getpid(), syscall.SIGINT)
				case 'c':
					// Ask the user for the new configuration
					c.GetConfig(func(cfg *HostConfig) {
						c.PromptUser("Select Wi-Fi mode [(A)uto/(s)tation/(h)ost]: ", func(s string) bool {
							switch s {
							case "", "a", "A":
								cfg.WifiMode = WifiModeAuto
							case "S", "s":
								cfg.WifiMode = WifiModeSTA
							case "h", "H":
								cfg.WifiMode = WifiModeAP
							default:
								return false
							}
							return true
						})
						c.PromptUser("Enter Wi-Fi SSID: ", func(s string) bool {
							cfg.WifiSSID = s
							return true
						})
						c.PromptUser("Enter Wi-Fi Password: ", func(s string) bool {
							cfg.WifiPassword = s
							c.SetConfig(cfg)
							return true
						})
					})
				case 'f':
					fmt.Fprintf(stdout, "flashing %s to host...\n", filepath.Base(info.AppBin))
					go func() {
						// Run this in a goroutine, since uploading will block
						// in order to ratelimit
						if err := flash(km, c); err != nil {
							fmt.Fprintf(stdout, "error flashing: %v\n", err)
						}
					}()
				case 'r':
					// Reboot the board
					if err := c.Reboot(); err != nil {
						fmt.Fprintf(stdout, "error rebooting host: %v\n", err)
					}
				case 'q':
					// Quit
					c.Close()
					return
				}
			case err := <-clientCh:
				if err != nil {
					if !*nonInteractiveArg && c.Host != nil && c.Host.Host != "" {
						// Try to reconnect
						hostFilter = c.Host.Host
						fmt.Fprintf(stdout, "disconnected from %s, trying to reconnect...\n", c.Host.Host)
						break PollingLoop
					}
					panic(err)
				}
				// non-nil err, requested exit
				return
			default:
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
}
