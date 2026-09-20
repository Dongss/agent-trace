package textfmt

import "testing"

func TestPathCollapsesBothSpellingsOfHome(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/someone/workspace/app":                            "~/workspace/app",
		"/home/someone/src":                                       "~/src",
		"~/.claude/projects/-Users-someone-workspace-app/s.jsonl": "~/.claude/projects/-~-workspace-app/s.jsonl",
		"/var/folders/xy/T/scratch":                               "/var/folders/xy/T/scratch",
		"":                                                        "",
	} {
		if got := Path(in); got != want {
			t.Errorf("Path(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := Clip("one\ntwo   three", 100); got != "one two   three" {
		t.Errorf("newlines not folded: %q", got)
	}
	if got := Clip("abcdefghij", 4); got != "abcd…" {
		t.Errorf("Clip = %q", got)
	}
	// Counted in runes, so a multi-byte string is not cut mid-character. Four
	// of these seven characters take two bytes each, so a byte count would cut
	// one in half and produce something that is not text.
	if got := Clip("ünïcödé", 3); got != "ünï…" {
		t.Errorf("Clip on runes = %q", got)
	}
	if got := Clip("anything", 0); got != "" {
		t.Errorf("Clip to zero = %q", got)
	}
}
