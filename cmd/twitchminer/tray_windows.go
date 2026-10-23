//go:build windows

package main

import (
	"context"
	"log/slog"
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
func runTray(opts trayOptions, m *miner.Miner, quit context.CancelFunc, done <-chan struct{}) {
	url := opts.url
	auto, err := newAutostart(opts.dataDir, opts.addr)
	if err != nil {
		slog.Warn("autostart unavailable", "err", err)
	} else if err := auto.Sync(); err != nil {
		slog.Warn("updating the autostart entry failed", "err", err)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	systray.Run(func() {
		systray.SetIcon(trayIcon())
		systray.SetTitle("TwitchMiner")
		systray.SetTooltip("TwitchMiner")

		open := systray.AddMenuItem("Open dashboard", "Open "+url)
		pause := systray.AddMenuItem("Pause mining", "Stop watching until resumed")
		startup := systray.AddMenuItemCheckbox("Start with Windows", "Start TwitchMiner in the tray when you sign in", auto != nil && auto.Enabled())
		if auto == nil || auto.temporary {
			startup.SetTooltip("Only available in a built twitchminer.exe")
			startup.Disable()
		}
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
				case <-startup.ClickedCh:
					on := !startup.Checked()
					if err := auto.Set(on); err != nil {
						slog.Error("changing autostart failed", "err", err)
						showError("Couldn't change Start with Windows:\n\n" + err.Error())
						break
					}
					if on {
						startup.Check()
						slog.Info("TwitchMiner will start when you sign in")
					} else {
						startup.Uncheck()
						slog.Info("TwitchMiner won't start when you sign in")
					}
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
	showError(msg)
}

func showError(msg string) {
	text, _ := windows.UTF16PtrFromString(msg)
	title, _ := windows.UTF16PtrFromString("TwitchMiner")
	windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
}
