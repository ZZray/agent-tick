//go:build windows

package main

// 贴边吸附与自动隐藏（像 QQ 那样）：
//   - 拖到工作区左 / 右 / 上边缘附近松手，窗口吸过去贴齐，记为“停靠”。
//   - 停靠后鼠标离开一会儿，面板缩成贴边的小标签（显示各会话进度）；鼠标碰到标签就展开。
//   - 收着时来了消息，标签变成气泡显示内容（页面负责）；从托盘叫回时展开并停留几秒。
//   - 前台有全屏程序（游戏、视频、演示）时整个窗口让开，退出全屏后回来。
// 窗口移动在宿主侧逐帧 SetWindowPos，跨线程调用 Windows 允许，不依赖网页。

import (
	"fmt"
	"math"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32                       = windows.NewLazySystemDLL("shell32.dll")
	pSHQueryUserNotificationState = shell32.NewProc("SHQueryUserNotificationState")
)

const (
	snapDist   = 20 // 离边多近松手算吸附（逻辑像素）
	tuckDelay  = 650 * time.Millisecond
	untuckWait = 120 * time.Millisecond // 鼠标在窄边停这么久才滑出，免得路过误触
	slideDur   = 200 * time.Millisecond
)

var (
	isDragging atomic.Bool
	sliding    atomic.Bool
)

type dockRuntime struct {
	lastInside time.Time
	enteredAt  time.Time // 收起状态下鼠标进入窄边的时刻
	holdHover  bool      // 等你：一直露着，直到鼠标进来过再离开
	holdUntil  time.Time // 做完类通知：露到这个时间
}

func (f *floatWin) dockEdge() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set.Dock
}

func (f *floatWin) autoHide() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.set.NoAutoHide
}

func (f *floatWin) fullscreenHide() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.set.NoFullscreenHide
}

// dockPos 算停靠时的位置：贴齐那条边，另一个方向保持原位（限制在工作区内）。
// 收起时窗口缩成小标签，整块仍在屏幕内，所以收起和展开用同一套位置
func (f *floatWin) dockPos(edge string, r rect) (int32, int32) {
	wa := f.workArea()
	w, h := r.Right-r.Left, r.Bottom-r.Top
	x := max(wa.Left, min(r.Left, wa.Right-w))
	y := max(wa.Top, min(r.Top, wa.Bottom-h))
	switch edge {
	case "left":
		x = wa.Left
	case "right":
		x = wa.Right - w
	case "top":
		y = wa.Top
	}
	return x, y
}

// slideTo 用 ease-out 把窗口移到目标位置；新的滑动会接着当前位置继续，不会跳
func (f *floatWin) slideTo(x, y int32) {
	sliding.Store(true)
	defer sliding.Store(false)
	r := f.windowRect()
	x0, y0 := float64(r.Left), float64(r.Top)
	if int32(x0) == x && int32(y0) == y {
		return
	}
	start := time.Now()
	for {
		t := math.Min(1, float64(time.Since(start))/float64(slideDur))
		e := 1 - math.Pow(1-t, 3) // ease-out cubic
		nx := int32(math.Round(x0 + (float64(x)-x0)*e))
		ny := int32(math.Round(y0 + (float64(y)-y0)*e))
		pSetWindowPos.Call(f.hwnd, 0, uintptr(nx), uintptr(ny), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
		if t >= 1 {
			return
		}
		time.Sleep(8 * time.Millisecond)
	}
}

// setTucked 收起 / 展开：只改状态并告诉页面；页面换成标签或完整面板，
// 再通过 tickFit 报告新尺寸，窗口跟着内容逐帧变形（见 fit）
func (f *floatWin) setTucked(on bool) {
	if f.dockEdge() == "" || f.tucked.Load() == on {
		return
	}
	f.tucked.Store(on)
	f.notifyDock()
}

// notifyDock 告诉页面当前停靠状态，页面据此画露在外面的那条窄边
func (f *floatWin) notifyDock() {
	edge, tucked := f.dockEdge(), f.tucked.Load()
	f.dispatch(func() {
		f.w.Eval(fmt.Sprintf("window.tickDock && tickDock(%q, %t)", edge, tucked))
	})
}

// afterDrag 拖动松手后：靠近边缘就吸过去并停靠，否则取消停靠
func (f *floatWin) afterDrag() {
	r := f.windowRect()
	wa := f.workArea()
	d := int32(math.Round(snapDist * f.scale()))
	edge := ""
	switch {
	case r.Left-wa.Left <= d:
		edge = "left"
	case wa.Right-r.Right <= d:
		edge = "right"
	case r.Top-wa.Top <= d:
		edge = "top"
	}
	f.mu.Lock()
	f.set.Dock = edge
	f.mu.Unlock()
	// 拖的是收起的小标签、松手还在边上：继续收着，只是沿边挪了位置；拖离边缘就展开成完整面板
	if edge == "" {
		f.tucked.Store(false)
	}
	f.dock.lastInside = time.Now()
	f.notifyDock()
	if edge != "" {
		x, y := f.dockPos(edge, r)
		f.slideTo(x, y)
	}
	f.rememberPos()
	f.pushHostState() // 拖到了屏幕另一半，抽屉要换边
}

// peek 有通知时把收起的窗口露出来。untilHover：等你类，鼠标进来过再离开才收回
func (f *floatWin) peek(untilHover bool, keep time.Duration) {
	if f.dockEdge() == "" {
		return
	}
	if untilHover {
		f.dock.holdHover = true
	} else {
		f.dock.holdUntil = time.Now().Add(keep)
	}
	f.dock.lastInside = time.Now()
	if f.tucked.Load() {
		f.setTucked(false)
	}
}

// dockLoop 轮询鼠标位置和全屏状态。只在一个 goroutine 里改 f.dock，避免加锁
func (f *floatWin) dockLoop() {
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()
	n := 0
	for range tick.C {
		n++
		if n%12 == 0 {
			f.checkFullscreen()
		}
		select {
		case req := <-f.dockReq:
			req()
		default:
		}
		edge := f.dockEdge()
		if edge == "" || isDragging.Load() || sliding.Load() || !f.visible() {
			continue
		}
		var c point
		pGetCursorPos.Call(uintptr(unsafe.Pointer(&c)))
		r := f.windowRect()
		m := int32(math.Round(6 * f.scale()))
		inside := c.X >= r.Left-m && c.X < r.Right+m && c.Y >= r.Top-m && c.Y < r.Bottom+m
		now := time.Now()
		if f.tucked.Load() {
			// 收起时只认露出来的那条窄边（加上贴着的屏幕边缘），避免路过窗口原位置就弹出
			if inside {
				if f.dock.enteredAt.IsZero() {
					f.dock.enteredAt = now
				} else if now.Sub(f.dock.enteredAt) >= untuckWait {
					f.dock.enteredAt = time.Time{}
					f.dock.lastInside = now
					f.setTucked(false)
				}
			} else {
				f.dock.enteredAt = time.Time{}
			}
			continue
		}
		if inside {
			f.dock.lastInside = now
			f.dock.holdHover = false // 等你：鼠标来过了，离开后就可以收
			continue
		}
		if !f.autoHide() || f.dock.holdHover || now.Before(f.dock.holdUntil) {
			continue
		}
		if now.Sub(f.dock.lastInside) >= tuckDelay {
			f.setTucked(true)
		}
	}
}

// checkFullscreen 前台是全屏程序或演示模式时让开；回到普通桌面后再出来
func (f *floatWin) checkFullscreen() {
	var state int32
	if r, _, _ := pSHQueryUserNotificationState.Call(uintptr(unsafe.Pointer(&state))); r != 0 {
		return
	}
	busy := state == 2 || state == 3 || state == 4 // BUSY / D3D 全屏 / 演示模式
	switch {
	case busy && f.fullscreenHide() && f.visible() && !f.fsHidden.Load():
		f.fsHidden.Store(true)
		f.dispatch(func() {
			pShowWindow.Call(f.hwnd, swHide)
			f.w.Eval("window.tickVisible && tickVisible(false)")
		})
	case !busy && f.fsHidden.Load():
		f.fsHidden.Store(false)
		f.dispatch(func() { f.show(false) })
	}
}
