//go:build windows

package main

// 完整面板：第二个本地窗口（WebView2），无边框、可缩放、进任务栏、记住大小位置。
// 标题栏由页面自己画（拖动区 + 窗口按钮），系统只保留阴影、圆角和缩放边框。
// 和悬浮窗用同一套网页，只是换成面板视图。浏览器里打开仍然可用，作为备选。

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

var (
	pCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	pSendMessage              = user32.NewProc("SendMessageW")
	pIsIconic                 = user32.NewProc("IsIconic")
	pIsZoomed                 = user32.NewProc("IsZoomed")
)

const (
	wmSetIcon     = 0x80
	wmSize        = 0x05
	wmNCCalcSize  = 0x83
	swMinimize    = 6
	swMaximize    = 3
	swRestore     = 9
	sizeMinimized = 1
)

type panelWin struct {
	mu      sync.Mutex
	opening bool // 正在创建窗口
	w       webview2.WebView
	hwnd    uintptr
	orig    uintptr
	maxed   bool // 上次告诉页面的最大化状态，避免缩放时反复推送
	dragEnd atomic.Bool
}

func zoomed(h uintptr) bool { r, _, _ := pIsZoomed.Call(h); return r != 0 }

// tellMax 把最大化状态告诉页面，好切换「最大化 / 还原」按钮
func (p *panelWin) tellMax(force bool) {
	m := zoomed(p.hwnd)
	if m == p.maxed && !force {
		return
	}
	p.maxed = m
	p.w.Eval(fmt.Sprintf("window.tickWinState && tickWinState({max:%t})", m))
}

// drag 无边框窗口的拖动：和悬浮窗一样由宿主按真实光标挪窗口。
// 最大化时按住标题栏拖出去先还原，并让光标停在窗口宽度上相同比例的位置；
// 松开时光标贴在屏幕顶边就最大化，跟系统标题栏的手感一致
func (p *panelWin) drag() {
	h := p.hwnd
	var start, cur point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&start)))
	var r rect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	moved := false
	deadline := time.Now().Add(2 * time.Minute)
	for !p.dragEnd.Load() && time.Now().Before(deadline) {
		time.Sleep(8 * time.Millisecond)
		pGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
		dx, dy := cur.X-start.X, cur.Y-start.Y
		if !moved {
			if dx*dx+dy*dy < 16 { // 小于 4px 当作点击，双击最大化不会被拖动打断
				continue
			}
			moved = true
			if zoomed(h) {
				frac := float64(start.X-r.Left) / float64(max(r.Right-r.Left, 1))
				pShowWindow.Call(h, swRestore)
				pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
				w := r.Right - r.Left
				r.Left = start.X - int32(frac*float64(w))
				r.Top = start.Y - 18 // 光标落在标题栏中间
			}
		}
		pSetWindowPos.Call(h, 0, uintptr(r.Left+dx), uintptr(r.Top+dy), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
	}
	if !moved {
		return
	}
	mon, _, _ := pMonitorFromWindow.Call(h, 2)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	if cur.Y <= mi.Monitor.Top+1 {
		pShowWindow.Call(h, swMaximize)
	}
}

var thePanel = &panelWin{}

// hicon 用内存里的 PNG 做窗口图标（Vista 起 CreateIconFromResourceEx 直接认 PNG）
func hicon(size int) uintptr {
	b := iconPNG(size, appIcon)
	h, _, _ := pCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 1, 0x30000, uintptr(size), uintptr(size), 0)
	return h
}

// openPanel 打开完整面板；已经开着就拿到前面来。
// 每个 WebView2 窗口在自己的线程上跑自己的消息循环
func openPanel(srv *Server, f *floatWin, hash string) {
	p := thePanel
	p.mu.Lock()
	if p.opening { // 窗口还在创建（要一两秒），这期间再点不要开第二个
		p.mu.Unlock()
		return
	}
	if p.w != nil {
		w, hwnd := p.w, p.hwnd
		p.mu.Unlock()
		w.Dispatch(func() {
			if r, _, _ := pIsIconic.Call(hwnd); r != 0 {
				pShowWindow.Call(hwnd, 9) // SW_RESTORE
			}
			pSetForegroundWindow.Call(hwnd)
			if hash != "" {
				w.Eval(fmt.Sprintf("location.hash = %q", hash))
			}
		})
		return
	}
	p.opening = true
	p.mu.Unlock()
	go func() {
		runtime.LockOSThread()
		f.mu.Lock()
		pr := f.set.Panel
		f.mu.Unlock()
		width, height := uint(1180), uint(760)
		if pr.W > 0 && pr.H > 0 {
			width, height = uint(pr.W), uint(pr.H)
		}
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:      f.dataPath,
			AutoFocus:     true,
			WindowOptions: webview2.WindowOptions{Title: "Tick · 面板", Width: width, Height: height, Center: pr.W == 0},
		})
		if w == nil {
			log.Printf("面板窗口创建失败，改用浏览器打开")
			p.mu.Lock()
			p.opening = false
			p.mu.Unlock()
			_ = openExternal(fmt.Sprintf("http://127.0.0.1:%d/%s", srv.port, hash))
			return
		}
		hwnd := uintptr(w.Window())
		dark := int32(1)
		pDwmSetWindowAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&dark)), 4) // 深色窗口边框
		pSendMessage.Call(hwnd, wmSetIcon, 0, hicon(16))
		pSendMessage.Call(hwnd, wmSetIcon, 1, hicon(32))
		if pr.W > 0 {
			pSetWindowPos.Call(hwnd, 0, uintptr(pr.X), uintptr(pr.Y), uintptr(pr.W), uintptr(pr.H), swpNoZOrder)
		}
		w.SetSize(720, 460, webview2.HintMin)
		// 关窗时记住大小和位置（最小化状态下不记）
		p.mu.Lock()
		p.w, p.hwnd, p.opening = w, hwnd, false
		p.mu.Unlock()
		p.orig, _, _ = pGetWindowLongPtr.Call(hwnd, uintptr(gwlpWndProcU()))
		pSetWindowLongPtr.Call(hwnd, uintptr(gwlpWndProcU()), syscall.NewCallback(func(h, msg, wp, lp uintptr) uintptr {
			switch msg {
			case wmNCCalcSize:
				// 无边框：让系统照常算出左右下的缩放边框，只把顶部标题栏拿掉。
				// 保留 WS_THICKFRAME，所以仍有系统阴影、圆角、Aero 贴靠和边缘缩放
				if wp != 0 {
					rc := *(**rect)(unsafe.Pointer(&lp)) // NCCALCSIZE_PARAMS 的第一个成员就是 rgrc[0]
					top, left := rc.Top, rc.Left
					r, _, _ := pCallWindowProc.Call(p.orig, h, msg, wp, lp)
					rc.Top = top
					if zoomed(h) { // 最大化时窗口会超出屏幕一圈边框宽度，顶部要让回来
						rc.Top += rc.Left - left
					}
					return r
				}
			case wmSize:
				if wp != sizeMinimized {
					p.tellMax(false)
				}
			case wmClose:
				// 关窗时记住大小和位置（最小化、最大化状态下不记，保留上次的正常尺寸）
				if r, _, _ := pIsIconic.Call(h); r == 0 && !zoomed(h) {
					var rc rect
					pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&rc)))
					f.mu.Lock()
					f.set.Panel = panelRect{rc.Left, rc.Top, rc.Right - rc.Left, rc.Bottom - rc.Top}
					f.mu.Unlock()
					f.saveSettings()
				}
			}
			r, _, _ := pCallWindowProc.Call(p.orig, h, msg, wp, lp)
			return r
		}))
		// 换了 NCCALCSIZE 的处理，让系统立刻按新规则重算一次框架
		pSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged)
		_ = w.Bind("tickOpen", func(url string) error { return openExternal(url) })
		_ = w.Bind("tickFloat", func() { f.dispatch(f.summon) })
		// 页面自己画的标题栏：拖动、最小化、最大化 / 还原、关闭。绑定函数在窗口线程上调用
		_ = w.Bind("tickWin", func(act string) {
			switch act {
			case "drag":
				p.dragEnd.Store(false)
				go p.drag()
			case "dragEnd":
				p.dragEnd.Store(true)
			case "min":
				pShowWindow.Call(hwnd, swMinimize)
			case "max":
				if zoomed(hwnd) {
					pShowWindow.Call(hwnd, swRestore)
				} else {
					pShowWindow.Call(hwnd, swMaximize)
				}
			case "close":
				pPostMessage.Call(hwnd, wmClose, 0, 0)
			case "state":
				p.tellMax(true)
			}
		})
		w.Navigate(fmt.Sprintf("http://127.0.0.1:%d/?host=panel%s", srv.port, hash))
		w.Run()
		p.mu.Lock()
		p.w, p.hwnd = nil, 0
		p.mu.Unlock()
		w.Destroy()
	}()
}

type panelRect struct{ X, Y, W, H int32 }
