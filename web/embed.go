// Package web embeds the compiled frontend (web/dist) into the binary.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
