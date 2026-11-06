// Package web embeds the frontend so the server ships as a single binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static returns the frontend files rooted at static/.
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		// Only possible if the embed directive above is broken: fail at startup, not at request time.
		panic(err)
	}
	return sub
}
