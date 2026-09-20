package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Dongss/agent-trace/internal/selfupdate"
	"github.com/Dongss/agent-trace/internal/version"
)

// updateTimeout bounds the whole operation. A release archive is tens of
// megabytes, so this is generous on purpose: the failure worth guarding
// against is a stalled connection, not a slow one.
const updateTimeout = 10 * time.Minute

// update downloads the latest release for this platform, verifies its SHA-256
// against the published checksums, and swaps it in place of the running
// binary. Nothing on disk changes until the download has been verified, so a
// network failure or a bad checksum leaves the current version as it was.
func update(out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	current := version.String()
	fmt.Fprintf(out, "agtrace %s: checking for a newer release…\n", current)

	res, err := selfupdate.Run(ctx, selfupdate.Options{Current: current})
	if err != nil {
		return err
	}
	if !res.Updated {
		fmt.Fprintf(out, "already on the latest release (%s)\n", res.To)
		return nil
	}
	fmt.Fprintf(out, "updated %s → %s\n", res.From, res.To)
	fmt.Fprintf(out, "installed %s\n", res.Path)
	fmt.Fprintln(out, "a server already running keeps the old version until it is restarted")
	return nil
}
