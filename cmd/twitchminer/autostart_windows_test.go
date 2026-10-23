//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// testAutostart points at a scratch key so the real Run key is never
// touched.
func testAutostart(t *testing.T, a *autostart) *autostart {
	t.Helper()
	const scratch = `Software\TwitchMinerTest`
	a.keyPath = scratch + `\Run`
	a.temporary = false
	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, a.keyPath)
		registry.DeleteKey(registry.CURRENT_USER, scratch)
	})
	return a
}

func TestAutostartCommand(t *testing.T) {
	a, err := newAutostart("data", defaultAddr)
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs("data")
	if !strings.Contains(a.command, abs) || !strings.Contains(a.command, "-open=false") {
		t.Fatalf("command should use the absolute data folder and not open a browser: %s", a.command)
	}
	if strings.Contains(a.command, "-addr") {
		t.Fatalf("the default address shouldn't be repeated: %s", a.command)
	}
	if b, _ := newAutostart("data", "0.0.0.0:9000"); !strings.Contains(b.command, "-addr 0.0.0.0:9000") {
		t.Fatalf("a custom address must be kept: %s", b.command)
	}
	// `go test` binaries live in a go-build temp folder, like `go run`.
	if !a.temporary {
		t.Error("a go-build executable should count as temporary")
	}
	if err := a.Set(true); err == nil {
		t.Error("enabling autostart for a temporary build must fail")
	}
}

func TestAutostartRegistry(t *testing.T) {
	a, err := newAutostart(`C:\Some Folder\data`, defaultAddr)
	if err != nil {
		t.Fatal(err)
	}
	a = testAutostart(t, a)

	if a.Enabled() {
		t.Fatal("scratch key should start empty")
	}
	if err := a.Set(true); err != nil {
		t.Fatal(err)
	}
	if !a.Enabled() {
		t.Fatal("entry should exist after enabling")
	}

	// An entry for a moved executable gets rewritten.
	k, _ := registry.OpenKey(registry.CURRENT_USER, a.keyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	k.SetStringValue(a.name, `"C:\old\twitchminer.exe"`)
	k.Close()
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	k, _ = registry.OpenKey(registry.CURRENT_USER, a.keyPath, registry.QUERY_VALUE)
	got, _, _ := k.GetStringValue(a.name)
	k.Close()
	if got != a.command {
		t.Fatalf("Sync left %q, want %q", got, a.command)
	}
	if !strings.Contains(got, `"C:\Some Folder\data"`) {
		t.Fatalf("paths with spaces must be quoted: %s", got)
	}

	if err := a.Set(false); err != nil {
		t.Fatal(err)
	}
	if a.Enabled() {
		t.Fatal("entry should be gone after disabling")
	}
	if err := a.Set(false); err != nil {
		t.Fatalf("disabling twice should be fine: %v", err)
	}
}
