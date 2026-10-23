// Command genicon writes the app icon as a multi-size .ico for embedding in
// twitchminer.exe. Usage: go run ./tools/genicon <out.ico>
package main

import (
	"fmt"
	"os"

	"github.com/AndreyKarm/TwitchMiner/internal/icon"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genicon <out.ico>")
		os.Exit(2)
	}
	// The sizes Windows asks for in Explorer, the taskbar and title bars.
	data := icon.ICO(16, 20, 24, 32, 40, 48, 64, 128, 256)
	if err := os.WriteFile(os.Args[1], data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
