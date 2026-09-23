package main

import (
	"slices"
	"strings"
	"testing"
)

// The help and the parser name the same words. A command that works and is
// not in the help is undiscoverable; a line in the help that the parser
// refuses is a lie. Nothing but this holds the two lists together.
func TestHelpListsEveryCommand(t *testing.T) {
	help := usage()
	i := strings.Index(help, "commands:\n")
	if i < 0 {
		t.Fatal("the help has no commands block")
	}
	block := help[i+len("commands:\n"):]
	if j := strings.Index(block, "\n\n"); j >= 0 {
		block = block[:j]
	}

	var listed []string
	for _, line := range strings.Split(block, "\n") {
		name, _, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || name == "" {
			continue
		}
		listed = append(listed, name)
	}
	if !slices.Equal(listed, commands) {
		t.Errorf("the help lists %v, the parser takes %v", listed, commands)
	}
}
