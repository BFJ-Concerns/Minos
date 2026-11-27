// Package deploy carries the shipped systemd user units into the minos
// binary, so `minos install-units` writes exactly the units this revision
// ships — the runs slice rendered from the configured memory envelope —
// without the deployment reading the repository tree.
package deploy

import "embed"

// Units holds deploy/systemd/user: the receiver, sweep, sweep timer and
// request path, sweep-after-run template and runs slice.
//
//go:embed systemd/user/*
var Units embed.FS
