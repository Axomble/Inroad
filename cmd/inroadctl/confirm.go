package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// confirm prints prompt followed by " [y/N]: " to out and reads one line from
// in, returning true only for an explicit y/yes (case-insensitive). autoYes
// (the --yes flag every destructive/credential-changing command accepts)
// short-circuits to true without prompting or reading anything — the escape
// hatch for scripted/non-interactive use.
func confirm(in io.Reader, out io.Writer, prompt string, autoYes bool) bool {
	if autoYes {
		return true
	}
	fmt.Fprintf(out, "%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
