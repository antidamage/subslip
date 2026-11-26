// Package subslip holds files that live at the repository root but are needed
// inside the binary. Today that is the commented settings template, which
// `subslip init` writes out as a starting .env.
//
// Used by cmd/subslip/init.go. The template itself is .env.example; edit it
// there and the binary picks it up at the next build.
package subslip

import _ "embed"

// EnvExample is the contents of .env.example.
//
//go:embed .env.example
var EnvExample string
