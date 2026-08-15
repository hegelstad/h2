package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckTestIsolation_RejectsRecordedHostDir(t *testing.T) {
	host := t.TempDir()
	if err := WriteMarker(host); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}

	old := hostH2Dirs
	hostH2Dirs = append([]string{canonPath(host)}, hostH2Dirs...)
	t.Cleanup(func() { hostH2Dirs = old })

	t.Setenv("H2_DIR", host)
	ResetResolveCache()
	t.Cleanup(ResetResolveCache)

	err := CheckTestIsolation()
	if err == nil {
		t.Fatal("expected error when ResolveDir points at a recorded host config dir")
	}
	if !strings.Contains(err.Error(), "real config dir") {
		t.Fatalf("error = %v, want substring %q", err, "real config dir")
	}
}

func TestCheckTestIsolation_AcceptsTempDir(t *testing.T) {
	dir := t.TempDir()
	if err := WriteMarker(dir); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}
	t.Setenv("H2_DIR", dir)
	ResetResolveCache()
	t.Cleanup(ResetResolveCache)

	if err := CheckTestIsolation(); err != nil {
		t.Fatalf("temp H2_DIR should be isolated: %v", err)
	}
}

func TestCheckTestIsolation_AcceptsInvalidH2DIR(t *testing.T) {
	t.Setenv("H2_DIR", t.TempDir()) // no marker
	ResetResolveCache()
	t.Cleanup(ResetResolveCache)

	if err := CheckTestIsolation(); err != nil {
		t.Fatalf("unresolvable H2_DIR is isolated (not the host dir): %v", err)
	}
}

func TestCheckTestIsolation_RejectsWalkUpToHostDir(t *testing.T) {
	host := t.TempDir()
	if err := WriteMarker(host); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}
	nested := filepath.Join(host, "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	old := hostH2Dirs
	hostH2Dirs = append([]string{canonPath(host)}, hostH2Dirs...)
	t.Cleanup(func() { hostH2Dirs = old })

	t.Setenv("H2_DIR", "")
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	ResetResolveCache()
	t.Cleanup(ResetResolveCache)

	err = CheckTestIsolation()
	if err == nil {
		t.Fatal("expected error when walk-up from CWD lands on a recorded host config dir")
	}
	if !strings.Contains(err.Error(), "real config dir") {
		t.Fatalf("error = %v, want substring %q", err, "real config dir")
	}
}

func TestCheckTestIsolation_RejectsStaleHostCache(t *testing.T) {
	host := t.TempDir()
	if err := WriteMarker(host); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}
	isolated := t.TempDir()
	if err := WriteMarker(isolated); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}

	old := hostH2Dirs
	hostH2Dirs = append([]string{canonPath(host)}, hostH2Dirs...)
	t.Cleanup(func() { hostH2Dirs = old })

	t.Setenv("H2_DIR", host)
	ResetResolveCache()
	if _, err := ResolveDir(); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	t.Setenv("H2_DIR", isolated)
	// Intentionally do not reset the cache: env is isolated but the
	// cached ResolveDir still points at the host dir.

	err := CheckTestIsolation()
	if err == nil {
		t.Fatal("expected error when ResolveDir cache still holds the host config dir")
	}
	if !strings.Contains(err.Error(), "real config dir") {
		t.Fatalf("error = %v, want substring %q", err, "real config dir")
	}
	ResetResolveCache()
}

func TestSetupFakeHome_DoesNotResolveToHost(t *testing.T) {
	fakeHome := setupFakeHome(t)
	dir, err := ResolveDir()
	if err != nil {
		t.Fatalf("ResolveDir after setupFakeHome: %v", err)
	}
	fakeHome = canonPath(fakeHome)
	dir = canonPath(dir)
	if dir != filepath.Join(fakeHome, ".h2") && !strings.HasPrefix(dir+string(filepath.Separator), fakeHome+string(filepath.Separator)) {
		t.Fatalf("ResolveDir() = %s, want under fake home %s", dir, fakeHome)
	}
	if err := CheckTestIsolation(); err != nil {
		t.Fatalf("setupFakeHome left tests pointing at the host config dir: %v", err)
	}
}
