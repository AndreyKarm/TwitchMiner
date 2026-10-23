//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
)

// trayIcon draws the favicon's diamond at 32x32 and wraps the PNG in an ICO
// container, which is what Windows tray icons need.
func trayIcon() []byte {
	const size, samples = 32, 4
	bg := color.RGBA{0x1c, 0x1c, 0x1c, 0xff}
	pink := color.RGBA{0xd8, 0x8c, 0xde, 0xff}
	accent := color.RGBA{0xcc, 0x50, 0xd6, 0xff}
	outer := [][2]float64{{16, 3}, {27, 13.5}, {16, 29}, {5, 13.5}}
	inner := [][2]float64{{16, 3}, {21, 13.5}, {16, 29}, {11, 13.5}}

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			// Supersample each pixel for smooth edges.
			var r, g, b, a float64
			for sy := range samples {
				for sx := range samples {
					px := float64(x) + (float64(sx)+0.5)/samples
					py := float64(y) + (float64(sy)+0.5)/samples
					var c color.RGBA
					switch {
					case inPolygon(px, py, inner):
						c = accent
					case inPolygon(px, py, outer):
						c = pink
					case inRoundedSquare(px, py, size, 7):
						c = bg
					default:
						continue
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a += 255
				}
			}
			n := float64(samples * samples)
			if a > 0 {
				img.SetRGBA(x, y, color.RGBA{
					R: uint8(r / (a / 255)), G: uint8(g / (a / 255)), B: uint8(b / (a / 255)), A: uint8(a / n),
				})
			}
		}
	}

	var pngData bytes.Buffer
	png.Encode(&pngData, img)

	// ICO header, one directory entry, then the PNG itself.
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, [3]uint16{0, 1, 1})
	binary.Write(&ico, binary.LittleEndian, struct {
		W, H, Colors, Reserved uint8
		Planes, BPP            uint16
		Size, Offset           uint32
	}{size, size, 0, 0, 1, 32, uint32(pngData.Len()), 6 + 16})
	ico.Write(pngData.Bytes())
	return ico.Bytes()
}

func inPolygon(x, y float64, pts [][2]float64) bool {
	inside := false
	for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
		xi, yi, xj, yj := pts[i][0], pts[i][1], pts[j][0], pts[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
	}
	return inside
}

func inRoundedSquare(x, y, size, radius float64) bool {
	cx := min(max(x, radius), size-radius)
	cy := min(max(y, radius), size-radius)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}
