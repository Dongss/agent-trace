// Package textfmt shortens text for display.
//
// It is what remained after the redaction modes were removed: collapsing a home
// directory and clipping a preview are layout, not privacy. Neither hides
// anything — a transcript's contents reach the page as the CLI recorded them.
package textfmt

import (
	"regexp"
	"strings"
)

// homeDir matches a macOS or Linux home directory of any user, not just this
// one: a transcript can be rendered on a different machine than it came from.
var homeDir = regexp.MustCompile(`(/Users/|/home/)[A-Za-z0-9._\-]+`)

// slugHome matches the same thing after Claude Code has slugified it into a
// directory name — ~/.claude/projects/-Users-someone-workspace-app.
var slugHome = regexp.MustCompile(`(^|/)-(?:Users|home)-[A-Za-z0-9._]+`)

// Path collapses a home directory to "~", in both the real and the slugified
// spelling, so a path fits a column instead of spending a third of it on a
// prefix every row shares.
func Path(s string) string {
	s = homeDir.ReplaceAllString(s, "~")
	return slugHome.ReplaceAllString(s, "${1}-~")
}

// Clip shortens s to at most n runes, marking that it was cut. Previews exist
// so a reader can recognise a step, not so the content can be read back out.
func Clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
