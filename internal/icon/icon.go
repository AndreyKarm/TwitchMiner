// Package icon draws the TwitchMiner icon, the favicon's pink diamond on a
// rounded dark square, at any size, and packs it as PNG or ICO.
package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
)

var (
	background = color.RGBA{0x1c, 0x1c, 0x1c, 0xff}
	pink       = color.RGBA{0xd8, 0x8c, 0xde, 0xff}
	accent     = color.RGBA{0xcc, 0x50, 0xd6, 0xff}
)

// The shapes on a 32 x 32 grid, scaled to the requested size.
var (
	outer = [][2]float64{{16, 3}, {27, 13.5}, {16, 29}, {5, 13.5}}
	inner = [][2]float64{{16, 3}, {21, 13.5}, {16, 29}, {11, 13.5}}
)

// Draw renders the icon at size x size pixels. Each size is drawn directly,
// not scaled from a larger image, so small icons stay sharp.
func Draw(size int) *image.RGBA {
	const samples = 4
	scale := float64(size) / 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			// Supersample each pixel for smooth edges.
			var r, g, b, a float64
			for sy := range samples {
				for sx := range samples {
					px := (float64(x) + (float64(sx)+0.5)/samples) / scale
					py := (float64(y) + (float64(sy)+0.5)/samples) / scale
					var c color.RGBA
					switch {
					case inPolygon(px, py, inner):
						c = accent
					case inPolygon(px, py, outer):
						c = pink
					case inRoundedSquare(px, py, 32, 7):
						c = background
					default:
						continue
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a += 255
				}
			}
			if a == 0 {
				continue
			}
			covered := a / 255
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(r / covered), G: uint8(g / covered), B: uint8(b / covered),
				A: uint8(a / (samples * samples)),
			})
		}
	}
	return img
}

// PNG encodes the icon at one size.
func PNG(size int) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, Draw(size))
	return buf.Bytes()
}

// ICO packs the icon at several sizes into one .ico file, each entry stored
// as PNG (supported since Windows Vista).
func ICO(sizes ...int) []byte {
	images := make([][]byte, len(sizes))
	for i, s := range sizes {
		images[i] = PNG(s)
	}
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s) // 256 is stored as 0
		if s >= 256 {
			dim = 0
		}
		binary.Write(&ico, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, data := range images {
		ico.Write(data)
	}
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
