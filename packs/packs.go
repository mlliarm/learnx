// Package packs embeds the built-in tech pack definitions.
package packs

import "embed"

//go:embed *.yaml
var FS embed.FS
