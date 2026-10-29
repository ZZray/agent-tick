package main

// 托盘和通知用的图标，代码里画：浅灰圆角方块 + 深色对勾；有人在等时右上角加一个橙点。

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// iconPNG 画一张 size×size 的图标；dot 为 true 时带橙色提醒点
func iconPNG(size int, dot bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	bg := color.NRGBA{0xE5, 0xE5, 0xE6, 0xFF}
	fg := color.NRGBA{0x08, 0x09, 0x0A, 0xFF}
	orange := color.NRGBA{0xF2, 0x99, 0x4A, 0xFF}
	r := s * 0.24 // 圆角
	stroke := s * 0.105
	// 对勾两段，坐标按 32 格画再缩放
	k := s / 32
	ax, ay, bx, by, cx, cy := 9*k, 16.6*k, 13.6*k, 21.2*k, 23.2*k, 11*k
	dcx, dcy, dr := s*0.80, s*0.20, s*0.19
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			// 圆角矩形的有符号距离
			qx := math.Abs(px-s/2) - (s/2 - r - 0.5)
			qy := math.Abs(py-s/2) - (s/2 - r - 0.5)
			d := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
			a := math.Max(0, math.Min(1, 0.5-d))
			if a == 0 {
				continue
			}
			c := bg
			cd := math.Min(segDist(px, py, ax, ay, bx, by), segDist(px, py, bx, by, cx, cy)) - stroke/2
			ca := math.Max(0, math.Min(1, 0.5-cd))
			c = mix(c, fg, ca)
			if dot {
				dd := math.Hypot(px-dcx, py-dcy)
				// 先挖一圈底色当描边，再画橙点
				ring := math.Max(0, math.Min(1, 0.5-(dd-dr-s*0.06)))
				c = mix(c, color.NRGBA{0x08, 0x09, 0x0A, 0xFF}, ring)
				c = mix(c, orange, math.Max(0, math.Min(1, 0.5-(dd-dr))))
				a = math.Max(a, ring)
			}
			c.A = uint8(a * 255)
			img.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func mix(a, b color.NRGBA, t float64) color.NRGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t + 0.5) }
	return color.NRGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 0xFF}
}

// iconICO 把几张 PNG 打成 ICO（Vista 起支持 PNG 内嵌）
func iconICO(dot bool) []byte {
	sizes := []int{16, 32, 48, 64}
	pngs := make([][]byte, len(sizes))
	for i, sz := range sizes {
		pngs[i] = iconPNG(sz, dot)
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, sz := range sizes {
		_ = binary.Write(&b, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, Bits           uint16
			Size, Offset           uint32
		}{uint8(sz), uint8(sz), 0, 0, 1, 32, uint32(len(pngs[i])), uint32(offset)})
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		b.Write(p)
	}
	return b.Bytes()
}
