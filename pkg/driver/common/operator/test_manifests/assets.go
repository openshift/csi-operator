package test_manifests

import (
	"embed"
)

//go:embed *.yaml
var f embed.FS

// ReadFile reads and returns the content of the named file.
func ReadFileOrDie(name string) []byte {
	data, err := f.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return data
}
