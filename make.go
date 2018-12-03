package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func ResolveMakefileVariables(makefiles []string, vars ...string) (map[string]string, error) {

	dir := filepath.Dir(makefiles[0])

	tmpFile, err := ioutil.TempFile(dir, ".vars.*.mk")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpFile.Name())

	var buf bytes.Buffer
	for _, v := range makefiles {
		fmt.Fprintf(&buf, "include %s\n", v)
	}
	fmt.Fprintf(&buf, "MAKECMDGOALS =\n")
	targetName := fmt.Sprintf("%s_%d", os.Args[0], os.Getpid())
	fmt.Fprintf(&buf, "%s:\n", targetName)
	for _, v := range vars {
		fmt.Fprintf(&buf, "\t$(info %s%s$$${%s})\n", targetName, v, v)
	}
	if _, err := tmpFile.Write(buf.Bytes()); err != nil {
		tmpFile.Close()
		return nil, err
	}
	if err := tmpFile.Close(); err != nil {
		return nil, err
	}
	var stdout bytes.Buffer
	cmd := exec.Command("make", "-f", tmpFile.Name(), targetName)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(&stdout)
	values := make(map[string]string)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, targetName) {
			kv := line[len(targetName):]
			sep := strings.IndexByte(kv, '$')
			k := kv[:sep]
			v := kv[sep+1:]
			values[k] = v
		}
	}
	return values, nil
}
