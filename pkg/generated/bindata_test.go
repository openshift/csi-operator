package generated

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Check that bindata contains the current version of files in manifests/
func TestBindata(t *testing.T) {
	err := filepath.Walk("manifests", func(path string, info os.FileInfo, err error) error {
		if info == nil || info.IsDir() {
			return nil
		}
		bindataPath := strings.TrimPrefix(path, "manifests/")
		t.Logf("Evaluating file %s (%s)", path, bindataPath)

		fileContent, err := ioutil.ReadFile(path)
		if err != nil {
			t.Errorf("Error reading file %s: %s", path, err)
		}

		binContent, err := Asset(bindataPath)
		if err != nil {
			t.Errorf("Error reading bindata %s: %s", bindataPath, err)
		}

		if string(binContent) != string(fileContent) {
			t.Errorf("File %s is different, re-run 'make generate'", bindataPath)
		}

		return nil
	})
	if err != nil {
		t.Errorf("Error processing manifests")
	}

}
