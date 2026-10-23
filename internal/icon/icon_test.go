package icon

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestICOHasEverySize(t *testing.T) {
	sizes := []int{16, 32, 256}
	ico := ICO(sizes...)
	r := bytes.NewReader(ico)
	var header [3]uint16
	binary.Read(r, binary.LittleEndian, &header)
	if header != [3]uint16{0, 1, 3} {
		t.Fatalf("bad ICO header %v", header)
	}
	for _, want := range sizes {
		var e struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}
		binary.Read(r, binary.LittleEndian, &e)
		img, err := png.Decode(bytes.NewReader(ico[e.Offset : e.Offset+e.Size]))
		if err != nil {
			t.Fatal(err)
		}
		if got := img.Bounds().Dx(); got != want {
			t.Fatalf("entry is %dpx, want %d", got, want)
		}
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("%dpx: corners should be transparent", want)
		}
		if _, _, _, a := img.At(want/2, want/2).RGBA(); a == 0 {
			t.Errorf("%dpx: the centre should be drawn", want)
		}
	}
}
