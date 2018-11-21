package e2e

import (
	"log"
	"testing"

	f "github.com/operator-framework/operator-sdk/pkg/test"
)

func TestMain(m *testing.M) {
	log.Printf("TestMain started\n")
	f.MainEntry(m)
}
