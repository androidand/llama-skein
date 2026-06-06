package main

// UpstreamVersion is the upstream llama-swap version this build is based on.
// Updated automatically during rebase via `make rebase-upstream`.
// Format: semantic version (e.g., "0.2.22")
var UpstreamVersion = "0.2.22"

// SkeinVersion is the llama-skein incremental semantic version.
// Increments with each commit via the Makefile.
var SkeinVersion = "0.1.0"
