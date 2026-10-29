package main

// 托盘、窗口用的图标，代码里画，和网页里的 logo 一致：
// 深色圆角方块 + 翠绿进度环 + 白色对勾。托盘里的环是活的：弧长 = 进行中会话的总完成比例，
// 有人在等你时变橙；没有进行中的会话时环是满的。托盘闪动时用翠绿底的亮帧。

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

type iconTone int

const (
	toneGreen  iconTone = iota
	toneOrange          // 有人在等你
	toneFlash           // 托盘闪动的亮帧
)

// iconState 一张图标画什么：环走到多少（0–1），什么颜色
type iconState struct {
	Progress float64
	Tone     iconTone
}

// 应用图标（窗口、任务栏）：环走到 78%，和网页 logo 一致
var appIcon = iconState{Progress: 0.78}

func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// cover 把有符号距离换成覆盖率（1px 抗锯齿）
func cover(d float64) float64 { return math.Max(0, math.Min(1, 0.5-d)) }

func mix(a, b color.NRGBA, t float64) color.NRGBA {
	t = math.Max(0, math.Min(1, t))
	l := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t + 0.5) }
	return color.NRGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 0xFF}
}

// iconPNG 画一张 size×size 的图标；坐标按 64 格设计再缩放
func iconPNG(size int, st iconState) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	k := s / 64
	bgTop, bgBot := color.NRGBA{0x2A, 0x2D, 0x33, 0xFF}, color.NRGBA{0x0E, 0x0F, 0x11, 0xFF}
	arcA, arcB := color.NRGBA{0x7C, 0xF2, 0xB5, 0xFF}, color.NRGBA{0x22, 0xB5, 0x73, 0xFF}
	track := color.NRGBA{0x45, 0x48, 0x4E, 0xFF}
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	switch st.Tone {
	case toneOrange:
		arcA, arcB = color.NRGBA{0xFF, 0xC0, 0x7A, 0xFF}, color.NRGBA{0xF2, 0x99, 0x4A, 0xFF}
	case toneFlash: // 翠绿底，环和勾都是白的：在任务栏上一眼能看出在闪
		bgTop, bgBot = color.NRGBA{0x4A, 0xE2, 0x9A, 0xFF}, color.NRGBA{0x0E, 0x9F, 0x62, 0xFF}
		arcA, arcB, track = white, white, color.NRGBA{0x2C, 0xC0, 0x80, 0xFF}
	}
	p := math.Max(0, math.Min(1, st.Progress))
	if p > 0 && p < 0.06 {
		p = 0.06 // 刚开工也露一小段，别像没在跑
	}
	r := 15 * k // 方块圆角
	cx, cy, rr, rw := s/2, s/2, 19*k, 5.5*k
	if size <= 20 { // 托盘 16px：环和勾都加粗一点，不然糊
		rw, rr = 6.4*k, 19.5*k
	}
	end := p * 2 * math.Pi
	ex, ey := cx+rr*math.Sin(end), cy-rr*math.Cos(end) // 弧的末端（从 12 点顺时针）
	sx, sy := cx, cy-rr
	ck := 5.2 * k
	if size <= 20 {
		ck = 6.2 * k
	}
	ax, ay, bx, by, qx2, qy2 := 24*k, 32.5*k, 29.6*k, 38.1*k, 40.6*k, 26.3*k
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			// 圆角方块
			qx := math.Abs(px-s/2) - (s/2 - r - 0.5)
			qy := math.Abs(py-s/2) - (s/2 - r - 0.5)
			d := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
			a := cover(d)
			if a == 0 {
				continue
			}
			c := mix(bgTop, bgBot, (px+py)/(2*s))
			if size >= 32 && st.Tone != toneFlash { // 大图标加一圈很淡的内描边
				c = mix(c, color.NRGBA{0x5A, 0x5D, 0x63, 0xFF}, cover(math.Abs(d+0.75*k)-0.75*k)*0.6)
			}
			// 环的底轨
			ring := math.Abs(math.Hypot(px-cx, py-cy)-rr) - rw/2
			c = mix(c, track, cover(ring)*0.9)
			// 进度弧：角度落在 [0, end] 里按环算，否则按两端的圆头算
			if p > 0 {
				th := math.Atan2(px-cx, cy-py)
				if th < 0 {
					th += 2 * math.Pi
				}
				var ad float64
				if th <= end || p >= 1 {
					ad = ring
				} else {
					ad = math.Min(math.Hypot(px-sx, py-sy), math.Hypot(px-ex, py-ey)) - rw/2
				}
				c = mix(c, mix(arcA, arcB, th/(2*math.Pi)), cover(ad))
			}
			// 对勾
			cd := math.Min(segDist(px, py, ax, ay, bx, by), segDist(px, py, bx, by, qx2, qy2)) - ck/2
			c = mix(c, white, cover(cd))
			c.A = uint8(a * 255)
			img.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// iconICO 把几张 PNG 打成 ICO（Vista 起支持 PNG 内嵌）
func iconICO(st iconState) []byte {
	sizes := []int{16, 20, 24, 32, 48, 64}
	pngs := make([][]byte, len(sizes))
	for i, sz := range sizes {
		pngs[i] = iconPNG(sz, st)
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
