//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// The per-user Run key starts programs when the user signs in, without
// needing administrator rights.
const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "TwitchMiner"
)

// autostart manages the Run entry for this executable.
type autostart struct {
	keyPath string
	name    string
	command string
	// temporary is set for `go run` builds, whose executable is deleted on
	// exit and can't be started at sign-in.
	temporary bool
}

// newAutostart builds the command Windows runs at sign-in. The data folder
// is made absolute because programs started at sign-in don't run from this
// folder, and the dashboard isn't opened so signing in stays quiet.
func newAutostart(dataDir, addr string) (*autostart, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	data, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	args := []string{exe, "-data", data, "-open=false"}
	if addr != defaultAddr {
		args = append(args, "-addr", addr)
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	return &autostart{
		keyPath:   runKeyPath,
		name:      runValueName,
		command:   strings.Join(quoted, " "),
		temporary: strings.Contains(strings.ToLower(exe), `\go-build`),
	}, nil
}

// Enabled reports whether a Run entry exists.
func (a *autostart) Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, a.keyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(a.name)
	return err == nil
}

// Set adds or removes the Run entry.
func (a *autostart) Set(on bool) error {
	if on && a.temporary {
		return errors.New("autostart needs a built twitchminer.exe, not go run")
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, a.keyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(a.name, a.command)
	}
	if err := k.DeleteValue(a.name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// Sync rewrites an existing entry that points at an old location, for
// example after the executable was moved or updated elsewhere.
func (a *autostart) Sync() error {
	if a.temporary || !a.Enabled() {
		return nil
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, a.keyPath, registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	current, _, err := k.GetStringValue(a.name)
	k.Close()
	if err != nil || current == a.command {
		return err
	}
	return a.Set(true)
}
