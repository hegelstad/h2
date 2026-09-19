package external

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRunExternalTests_CleansBuildDirectory(t *testing.T) {
	for _, tc := range []struct {
		name      string
		buildCode int
		testCode  int
		wantCode  int
	}{
		{name: "success"},
		{name: "test failure", testCode: 7, wantCode: 7},
		{name: "build failure", buildCode: 17, wantCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			bin := t.TempDir()
			// The third argument to "go build -o" is the output path.
			script := fmt.Sprintf("#!/bin/sh\n: > \"$3\"\nexit %d\n", tc.buildCode)
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			originalBinary := h2Binary
			t.Cleanup(func() { h2Binary = originalBinary })
			called := false
			code := runExternalTests(func() int {
				called = true
				if _, err := os.Stat(h2Binary); err != nil {
					t.Errorf("test binary missing during run: %v", err)
				}
				return tc.testCode
			})
			if code != tc.wantCode || called != (tc.buildCode == 0) {
				t.Errorf("exit=%d, called=%v; want exit=%d, called=%v", code, called, tc.wantCode, tc.buildCode == 0)
			}
			entries, err := os.ReadDir(tmp)
			if err != nil || len(entries) != 0 {
				t.Errorf("temporary build directory leaked: entries=%v, err=%v", entries, err)
			}
		})
	}
}
