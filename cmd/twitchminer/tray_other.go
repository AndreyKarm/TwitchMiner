//go:build !windows

package main

import (
	"context"

	"github.com/AndreyKarm/TwitchMiner/internal/miner"
)

// The tray icon is only built on Windows; elsewhere the miner runs in the
// terminal or as a service.
const trayAvailable = false

func runTray(string, *miner.Miner, context.CancelFunc, <-chan struct{}) {}

func showFatal(string) {}
