package telegram

import (
	"fmt"
	"os"
	"testing"

	"h2/internal/config"
)

func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	dir, err := os.MkdirTemp("", "h2-tg-iso-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	if err := config.WriteMarker(dir); err != nil {
		panic(err)
	}
	os.Setenv("H2_DIR", dir)
	os.Setenv("H2_ROOT_DIR", dir)
	config.ResetResolveCache()
	if err := config.CheckTestIsolation(); err != nil {
		fmt.Fprintf(os.Stderr, "CheckTestIsolation: %v\n", err)
		return 1
	}
	return m.Run()
}
