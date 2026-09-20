package version

import (
	"strings"
	"testing"
)

func TestStringReportsTheVersion(t *testing.T) {
	got := String()
	if got == "" {
		t.Fatal("no version at all")
	}
	// Whatever the build, the release it came from has to be in there.
	if !strings.HasPrefix(got, Version) {
		t.Errorf("String() = %q, want it to start with %q", got, Version)
	}
	// A working-tree build says so; a release does not.
	if dirty() && !strings.HasSuffix(got, "-dev") {
		t.Errorf("a modified tree reported %q without the dev marker", got)
	}
	if !dirty() && strings.HasSuffix(got, "-dev") {
		t.Errorf("a clean tree reported %q as a working-tree build", got)
	}
}

func TestVersionLooksLikeARelease(t *testing.T) {
	if !strings.HasPrefix(Version, "v") {
		t.Errorf("Version = %q; the tag keeps its leading v so the CLI and the tag agree", Version)
	}
}
