package main

import (
	"bufio"
	"fmt"
	"strings"
)

func (c *Client) PromptUser(prompt string, isValid func(string) bool) string {
	for {
		fmt.Fprintf(c.Stdout(), prompt)
		r := bufio.NewReader(c.Stdin())
		st, _ := r.ReadString('\n')
		st = strings.TrimSpace(st)
		if isValid(st) {
			return st
		}
	}
}
