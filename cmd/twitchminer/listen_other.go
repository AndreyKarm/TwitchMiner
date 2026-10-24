//go:build !windows

package main

// Other systems refuse to let a program take a port another one already
// listens on, so there is never a rival to find.
func rivalOnPort(string) error { return nil }
