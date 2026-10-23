//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"os"
	"testing"
)

func TestTrayIconIsValidICO(t *testing.T) {
	ico := trayIcon()
	var header [3]uint16
	binary.Read(bytes.NewReader(ico), binary.LittleEndian, &header)
	if header != [3]uint16{0, 1, 1} {
		t.Fatalf("bad ICO header %v", header)
	}
	img, err := png.Decode(bytes.NewReader(ico[22:]))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Fatalf("icon is %v, want 32x32", b)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Error("corners should be transparent")
	}
	if out := os.Getenv("TRAY_ICON_OUT"); out != "" {
		os.WriteFile(out, ico[22:], 0o644)
	}
}
