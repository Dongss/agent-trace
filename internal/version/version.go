// Package version reports the build's identity.
package version

import "runtime/debug"

// Version is the release this source tree is working toward, bumped here and
// overridden by a release build:
//
//	go build -ldflags "-X github.com/Dongss/agent-trace/internal/version.Version=v0.1.0"
//
// The variable name and the stamping shape are the conventional ones, so
// whatever scripts/build.sh and goreleaser land here later will work
// unchanged.
//
// The default is the release being worked toward rather than "". An empty
// default is right once every build comes from a tag, but until then it
// reports a Go pseudo-version, which is worse than a slightly early number.
var Version = "v0.1.0"

// String returns the version, marked as a working-tree build when the source
// it was built from had uncommitted changes — a release comes off a clean tag,
// so only a local build picks up the suffix.
func String() string {
	v := Version
	if v == "" {
		v = "devel"
	}
	if dirty() {
		return v + "-dev"
	}
	return v
}

func dirty() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.modified" {
			return s.Value == "true"
		}
	}
	return false
}
