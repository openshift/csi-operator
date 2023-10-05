package assets

import (
	"embed"
)

//go:embed base/* common/* overlays/*
var f embed.FS

// ReadFile reads and returns the content of the named file.
func ReadFile(name string) ([]byte, error) {
	return f.ReadFile(name)
}
