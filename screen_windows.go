//go:build windows

package main

// 大屏窗口：独立的全屏窗口（view=screen），一块无限画布，每个会话一张可拖动的卡片，给扩展屏挂着看。
// 托盘菜单「大屏模式」开关（或 POST /api/desktop/screen）开关；Esc 或关掉开关退出。
// 有扩展屏时铺第一块非主屏，只有一块屏时铺悬浮窗所在的那块。卡片布局、平移缩放和位置记忆都在页面里，宿主只管窗口。

import (
	"log"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

var (
	pEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	pGetForegroundWindow = user32.NewProc("GetForegroundWindow")
)

var theScreen struct {
	mu      sync.Mutex
	opening bool // 正在创建窗口
	wanted  bool // 创建期间也记住最后一次开关请求
	closing bool // API 已请求关闭；关完时若又收到开启则重新打开
	w       webview2.WebView
	hwnd    uintptr
}

// enumExtra 在 EnumDisplayMonitors 里同步回调：记下第一块非主屏
var enumExtra struct {
	mu    sync.Mutex
	found bool
	rc    rect
}

var enumExtraCB = syscall.NewCallback(func(mon, hdc, rc, lp uintptr) uintptr {
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	if mi.Flags&1 == 0 { // MONITORINFOF_PRIMARY
		enumExtra.found, enumExtra.rc = true, mi.Monitor
		return 0 // 找到就停
	}
	return 1
})

// screenRect 大屏铺哪块屏：有扩展屏用扩展屏，否则用悬浮窗现在所在的那块
func screenRect(f *floatWin) (rect, string) {
	enumExtra.mu.Lock()
	enumExtra.found = false
	pEnumDisplayMonitors.Call(0, 0, enumExtraCB, 0)
	found, rc := enumExtra.found, enumExtra.rc
	enumExtra.mu.Unlock()
	if found {
		return rc, "扩展屏"
	}
	mon, _, _ := pMonitorFromWindow.Call(f.hwnd, 2)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	return mi.Monitor, "悬浮窗所在的屏"
}

func screenOn() bool {
	s := &theScreen
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w != nil || s.opening
}

// screenForeground 前台窗口就是大屏：它铺满一块屏，系统会当成“全屏程序”，悬浮窗和提醒卡不该因此让开
func screenForeground() bool {
	s := &theScreen
	s.mu.Lock()
	h := s.hwnd
	s.mu.Unlock()
	fg, _, _ := pGetForegroundWindow.Call()
	return h != 0 && fg == h
}

// setScreen 开 / 关大屏窗口。开着再开、关着再关都什么也不做
func (f *floatWin) setScreen(on bool) {
	s := &theScreen
	s.mu.Lock()
	s.wanted = on
	if !on {
		h := s.hwnd
		s.closing = h != 0
		s.mu.Unlock()
		if h != 0 {
			pPostMessage.Call(h, wmClose, 0, 0)
		}
		return
	}
	if s.w != nil || s.opening {
		s.mu.Unlock()
		return
	}
	s.opening = true
	s.mu.Unlock()
	go func() {
		runtime.LockOSThread()
		for { // 同一线程按最新开关请求重建；内部重开不覆写 wanted。
			w := webview2.NewWithOptions(webview2.WebViewOptions{
				DataPath:      f.dataPath, // 和悬浮窗共用一份浏览器进程
				AutoFocus:     true,
				WindowOptions: webview2.WindowOptions{Title: "Tick · 大屏", Width: 1280, Height: 720},
			})
			if w == nil {
				log.Printf("[大屏] 窗口创建失败")
				s.mu.Lock()
				s.opening = false
				s.mu.Unlock()
				return
			}
			h := uintptr(w.Window())
			pShowWindow.Call(h, swHide)
			pSetWindowLongPtr.Call(h, uintptr(gwlStyleU()), wsPopup|wsClipChildren)
			dark := int32(1)
			pDwmSetWindowAttribute.Call(h, 20, uintptr(unsafe.Pointer(&dark)), 4)
			pSendMessage.Call(h, wmSetIcon, 0, hicon(16))
			pSendMessage.Call(h, wmSetIcon, 1, hicon(32))
			_ = w.Bind("tickScreenClose", func() { pPostMessage.Call(h, wmClose, 0, 0) })
			rc, where := screenRect(f)
			s.mu.Lock()
			show := s.wanted
			if show {
				s.w, s.hwnd, s.opening = w, h, false
				// 不置顶：铺满一块屏的前台窗口系统会自动收起任务栏；别的窗口想盖住它也可以
				pSetWindowPos.Call(h, 0, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right-rc.Left), uintptr(rc.Bottom-rc.Top), swpFrameChanged|0x40) // SWP_SHOWWINDOW
				pSetForegroundWindow.Call(h)
			}
			s.mu.Unlock()
			if show {
				log.Printf("[大屏] 已打开，铺在%s %d,%d %dx%d", where, rc.Left, rc.Top, rc.Right-rc.Left, rc.Bottom-rc.Top)
				w.Navigate(pageURL(f.srv.port, "view=screen"))
				w.Run()
			} else {
				// Destroy 只投递 WM_CLOSE；泵完退出消息再复用线程，避免旧 WM_QUIT 中断下一次初始化。
				w.Destroy()
				w.Run()
			}
			s.mu.Lock()
			if show && !s.closing { // Esc / Alt+F4 是用户直接关闭，不自动重新打开。
				s.wanted = false
			}
			s.w, s.hwnd = nil, 0
			s.opening = true // Destroy 完成前由这一线程保留创建权。
			s.closing = false
			s.mu.Unlock()
			log.Printf("[大屏] 已关闭")
			s.mu.Lock()
			if !s.wanted {
				s.opening = false
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
		}
	}()
}
