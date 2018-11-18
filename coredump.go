package main

import (
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
)

// espcoredump.py dbg_corefile --core core.dump --core-format=raw ~/Source/esp/wifidev/build/blink.elf

func (c *Client) runEspCoredump(filename string, op string) error {
	info := c.ProjectInfo()
	espcoredumpPy := filepath.Join(info.IDFPath, "components", "espcoredump", "espcoredump.py")
	km := c.Stdin().(*keyboardMonitorReader).km
	cmd := exec.Command("python", espcoredumpPy, op, "--core="+filename, "--core-format=raw", info.AppElf)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	wait := make(chan struct{}, 1)
	var err error
	km.RunPaused(func() {
		err = cmd.Run()
		wait <- struct{}{}
	})
	<-wait
	return err
}

func (c *Client) DisplayCoreDump(data []byte) (del bool, err error) {
	// Write the dump to a file. Skip the initial magic number, since
	// espcoredump.py expects the dump without it
	tmpFile, err := ioutil.TempFile("", "core.*")
	if err != nil {
		return false, err
	}
	fileName := tmpFile.Name()
	defer os.Remove(fileName)
	if _, err := tmpFile.Write(data[4:]); err != nil {
		tmpFile.Close()
		return false, err
	}
	if err := tmpFile.Close(); err != nil {
		return false, err
	}
	var ret error
	c.PromptUser("Select what do to with this coredump [(V)iew/(g)db/(d)elete/(i)ignore]: ", func(s string) bool {
		switch s {
		case "v", "V", "":
			if err := c.runEspCoredump(fileName, "info_corefile"); err != nil {
				del = false
				ret = err
			}
		case "g", "G":
			if err := c.runEspCoredump(fileName, "dbg_corefile"); err != nil {
				del = false
				ret = err
			}
		case "d", "D":
			del = true
		case "i", "I":
			del = false
		default:
			// Not handled
			return false
		}
		return true
	})
	return del, ret
}
