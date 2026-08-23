package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// hostH2Dirs is the set of h2 directories that existed on the host when
// this process started (H2_DIR, walk-up from CWD, ~/.h2). Tests must not
// resolve to any of these — that is the real config tree.
var hostH2Dirs []string

func init() {
	recordHostH2Dirs()
}

func recordHostH2Dirs() {
	add := func(p string) {
		if p == "" {
			return
		}
		c := canonPath(p)
		for _, existing := range hostH2Dirs {
			if existing == c {
				return
			}
		}
		hostH2Dirs = append(hostH2Dirs, c)
	}

	add(os.Getenv("H2_DIR"))

	if cwd, err := os.Getwd(); err == nil {
		for dir := cwd; ; {
			if IsH2Dir(dir) || looksLikeH2Dir(dir) {
				add(dir)
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".h2"))
	}
}

func canonPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if ev, err := filepath.EvalSymlinks(abs); err == nil {
		return ev
	}
	return filepath.Clean(abs)
}

func rejectHostDir(dir string) error {
	got := canonPath(dir)
	for _, host := range hostH2Dirs {
		if got == host {
			return fmt.Errorf("test resolved H2_DIR to the real config dir %s; tests must not touch the real config dir. Set H2_DIR to a temp directory and call ResetResolveCache()", dir)
		}
	}
	return nil
}

// CheckTestIsolation reports whether the current process would use the
// host's real h2 config directory. It inspects both a populated ResolveDir
// cache and a fresh resolve of the current env, and does not write the
// cache (so calling it from setupFakeHome does not pin a resolved dir).
// A nil error means resolution failed (no h2 dir — isolated) or the
// resolved path is not the host tree. Tests must never touch the real
// config dir.
func CheckTestIsolation() error {
	if resolvedDir != "" {
		if err := rejectHostDir(resolvedDir); err != nil {
			return err
		}
	}
	dir, err := resolveDir()
	if err != nil {
		return nil
	}
	return rejectHostDir(dir)
}
