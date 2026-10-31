//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"math"
	"time"
	"unsafe"
)

type floatWidthDrag struct {
	side   string
	origin rect
	work   rect
	startX int32
	scale  float64
	width  float64
	moved  bool
	up     bool
	timer  *time.Timer
}

// boundedFloatWidth allows a small work area to be narrower than the normal minimum.
func boundedFloatWidth(width, available float64) float64 {
	hi := math.Max(1, math.Min(720, available))
	lo := math.Min(320, hi)
	return math.Max(lo, math.Min(width, hi))
}

func (f *floatWin) preferredWidth() float64 {
	f.mu.Lock()
	w := f.set.Width
	f.mu.Unlock()
	if w == 0 {
		return 340
	}
	return boundedFloatWidth(w, 720)
}

// floatWidth is called on the float UI thread, including inside pushHostState's dispatch.
func (f *floatWin) floatWidth() float64 {
	if f.widthDrag != nil {
		return f.widthDrag.width
	}
	wa := f.workArea()
	return boundedFloatWidth(f.preferredWidth(), float64(wa.Right-wa.Left)/f.scale())
}

func (d *floatWidthDrag) widthAt(cursorX int32) float64 {
	delta := float64(int64(cursorX)-int64(d.startX)) / d.scale
	available := float64(d.work.Right-d.origin.Left) / d.scale
	if d.side == "left" {
		delta = -delta
		available = float64(d.origin.Right-d.work.Left) / d.scale
	}
	return boundedFloatWidth(float64(d.origin.Right-d.origin.Left)/d.scale+delta, available)
}

func (d *floatWidthDrag) position(w, h int32) (int32, int32) {
	x, y := d.origin.Left, d.origin.Top
	if d.side == "left" {
		x = d.origin.Right - w
	}
	if d.up {
		y = d.origin.Bottom - h
	}
	return x, y
}

func (f *floatWin) useStripAnchor(long bool) bool {
	return f.widthDrag == nil && !long && now() < f.stripAnchorUntil.Load()
}

func (f *floatWin) resizeUp(origin, work rect) bool {
	if f.resH > 0 {
		return f.resUp
	}
	return (origin.Top+origin.Bottom)/2 > (work.Top+work.Bottom)/2
}

// checkWidthResize keeps the input/mode boundary separate from native cursor acquisition.
func (f *floatWin) checkWidthResize(side string) error {
	if side != "left" && side != "right" {
		return fmt.Errorf("拖宽边缘必须是 left 或 right")
	}
	f.mu.Lock()
	edge := f.set.Dock
	f.mu.Unlock()
	// Compact 是用户偏好；停靠自动展开时屏上可能已是完整面板，不能用它拒绝拖宽。
	if f.tucked.Load() || f.anchorOn || f.widthDrag != nil {
		return fmt.Errorf("当前悬浮窗形态不能拖宽")
	}
	if edge == side {
		return fmt.Errorf("停靠边缘不能拖宽")
	}
	return nil
}

func (f *floatWin) resizeStart(side string) (float64, error) {
	if err := f.checkWidthResize(side); err != nil {
		return 0, err
	}
	if !dragging.TryLock() {
		return 0, fmt.Errorf("窗口正在拖动")
	}
	var cursor point
	if ok, _, err := pGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor))); ok == 0 {
		dragging.Unlock()
		return 0, fmt.Errorf("读取拖宽起点失败：%w", err)
	}
	origin, work := f.windowRect(), f.workArea()
	d := &floatWidthDrag{side: side, origin: origin, work: work, startX: cursor.X, scale: f.scale(),
		up: f.resizeUp(origin, work)}
	d.width = d.widthAt(cursor.X)
	f.widthDrag = d
	isDragging.Store(true)
	// 与既有窗口拖动一样设两分钟上限，防止页面丢失松手事件后一直占有拖动锁。
	d.timer = time.AfterFunc(2*time.Minute, func() {
		f.dispatch(func() {
			if f.widthDrag == d {
				if _, err := f.resizeEnd(); err != nil {
					log.Printf("拖宽超时收尾失败：%v", err)
				}
			}
		})
	})
	return d.width, nil
}

func (f *floatWin) resizeStep() (float64, error) {
	d := f.widthDrag
	if d == nil {
		return f.floatWidth(), nil
	}
	var cursor point
	if ok, _, err := pGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor))); ok == 0 {
		return d.width, fmt.Errorf("读取拖宽位置失败：%w", err)
	}
	d.width = d.widthAt(cursor.X)
	d.moved = d.moved || cursor.X != d.startX
	// 在 active anchor 下直接落位；页面随后报告新换行高度时，fit 仍使用同一固定边。
	return d.width, f.fit(d.width, f.cssH)
}

func (f *floatWin) resizeEnd() (float64, error) {
	d := f.widthDrag
	if d == nil {
		return f.floatWidth(), nil
	}
	_, stepErr := f.resizeStep()
	width, err := f.finishWidthDrag(d, f.windowRect(), stepErr)
	f.pushHostState()
	return width, err
}

// finishWidthDrag owns persistence and lock release separately from cursor/native geometry.
func (f *floatWin) finishWidthDrag(d *floatWidthDrag, final rect, stepErr error) (float64, error) {
	if f.widthDrag != d {
		return f.preferredWidth(), nil
	}
	defer func() {
		if d.timer != nil {
			d.timer.Stop()
		}
		f.widthDrag = nil
		isDragging.Store(false)
		dragging.Unlock()
	}()
	f.mu.Lock()
	// A work-area clip alone must not replace a wider user preference.
	if stepErr == nil && d.moved && int32(math.Round(d.width*d.scale)) != d.origin.Right-d.origin.Left {
		f.set.Width = d.width
	}
	f.set.X, f.set.Y, f.set.HasPos = final.Left, final.Top, true
	f.mu.Unlock()
	return d.width, errors.Join(stepErr, f.saveSettings())
}
