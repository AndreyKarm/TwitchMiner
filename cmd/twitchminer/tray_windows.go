//go:build windows

package main

import (
	"context"
	"runtime"
	"syscall"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"github.com/AndreyKarm/TwitchMiner/internal/miner"
)

const trayAvailable = true

// runTray shows the tray icon and blocks until Quit is chosen (which calls
// quit) or the miner finishes (done closes).
func runTray(url string, m *miner.Miner, quit context.CancelFunc, done <-chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	systray.Run(func() {
		systray.SetIcon(trayIcon())
		systray.SetTitle("TwitchMiner")
		systray.SetTooltip("TwitchMiner")

		open := systray.AddMenuItem("Open dashboard", "Open "+url)
		pause := systray.AddMenuItem("Pause mining", "Stop watching until resumed")
		systray.AddSeparator()
		exit := systray.AddMenuItem("Quit", "Stop mining and exit")
		systray.SetOnTapped(func() { openBrowser(url) })

		go func() {
			sync := func() {
				if m.Paused() {
					pause.SetTitle("Resume mining")
					systray.SetTooltip("TwitchMiner (paused)")
				} else {
					pause.SetTitle("Pause mining")
					systray.SetTooltip("TwitchMiner")
				}
			}
			// The dashboard can pause too, so keep the label in step.
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-open.ClickedCh:
					openBrowser(url)
				case <-pause.ClickedCh:
					m.SetPaused(!m.Paused())
					sync()
				case <-exit.ClickedCh:
					quit()
				case <-tick.C:
					sync()
				case <-done:
					systray.Quit()
					return
				}
			}
		}()
	}, nil)
}

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	getConsoleWindow = kernel32.NewProc("GetConsoleWindow")
)

// showFatal shows a message box, but only when there's no console window
// the error was already printed to.
func showFatal(msg string) {
	if hwnd, _, _ := getConsoleWindow.Call(); hwnd != 0 {
		return
	}
	text, _ := windows.UTF16PtrFromString(msg)
	title, _ := windows.UTF16PtrFromString("TwitchMiner")
	windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
}
