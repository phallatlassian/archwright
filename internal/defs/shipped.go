package defs

import (
	"embed"
	"io/fs"
)

//go:embed all:shipped
var shippedFS embed.FS

// Shipped is the read-only definitions that ship inside the binary.
func Shipped() fs.FS {
	sub, err := fs.Sub(shippedFS, "shipped")
	if err != nil {
		panic(err)
	}
	return sub
}
