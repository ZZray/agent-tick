//go:build windows

package main

// 屏幕跑马灯：托盘“显示与通知”里的可选开关（默认关）。开启后，提醒卡（见 toast_windows.go）同时转给这里，
// 在悬浮窗所在屏幕工作区的上方开一条全透明、置顶、不抢焦点的窗口（view=marquee），
// 页面里一条光带 + 滚动文字，播完自动消失。
// 窗口不能点穿：带 WS_EX_LAYERED 的窗口里 WebView2 画不出透明（实测整块变白），而 DWM 透明与分层窗口互斥。
// 所以窗口接收鼠标，由页面处理：鼠标停在光带上时不消失（光效照常播放）；点一下收起并打开对应会话（tickMarqueeOpen，同右下角卡片）。

import (
	"log"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

const (
	marqueeCSSW = 1000.0 // 窗口宽（CSS px），超出工作区时按工作区的 92% 收窄
	marqueeCSSH = 96.0   // 光带 76 + 上下各 10；窗口会挡住下面的点击，所以不留大余量
)

type marqueeWin struct {
	mu      sync.Mutex
	w       webview2.WebView
	hwnd    uintptr
	opening bool
	ready   bool
	queue   []string
	gen     int // 每次显示 / 隐藏加一，过期的销毁计时器据此作废
}

var theMarquee = &marqueeWin{}

func (f *floatWin) marqueeOn() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set.Marquee
}

// marquee 把一张提醒卡转给跑马灯窗口。“勾了一项”太碎，不上屏幕；其余提醒按开关决定。
// card 已由 toast() 校验为 JSON 对象。
func (f *floatWin) marquee(kind, card string) {
	if kind == "item" || !f.marqueeOn() {
		return
	}
	t := theMarquee
	t.mu.Lock()
	if t.w != nil && t.ready {
		w, h := t.w, t.hwnd
		t.gen++ // 有新内容：作废待执行的销毁计时器
		t.mu.Unlock()
		w.Dispatch(func() {
			t.place(f, h)
			w.Eval("window.tickMarqueeAdd && tickMarqueeAdd(" + card + ")")
		})
		return
	}
	if len(t.queue) < 8 {
		t.queue = append(t.queue, card)
	}
	if t.opening {
		t.mu.Unlock()
		return
	}
	t.opening = true
	t.mu.Unlock()
	go t.open(f)
}

// open 在自己的线程上建窗口并跑消息循环，直到被销毁
func (t *marqueeWin) open(f *floatWin) {
	runtime.LockOSThread()
	w := newClearWebView(webview2.WebViewOptions{
		DataPath:      f.dataPath,
		WindowOptions: webview2.WindowOptions{Title: "Tick 跑马灯", Width: 1000, Height: 96},
	})
	if w == nil {
		log.Printf("跑马灯窗口创建失败")
		t.mu.Lock()
		t.opening, t.queue = false, nil
		t.mu.Unlock()
		return
	}
	h := uintptr(w.Window())
	pShowWindow.Call(h, swHide)
	pSetWindowLongPtr.Call(h, uintptr(gwlStyleU()), wsPopup|wsClipChildren)
	pSetWindowLongPtr.Call(h, uintptr(gwlExStyleU()), wsExToolWindow|wsExTopmost|wsExNoActivate)
	clearFrame(h)
	_ = w.Bind("tickMarqueeReady", func() {
		t.mu.Lock()
		t.ready, t.opening = true, false
		q := t.queue
		t.queue = nil
		t.mu.Unlock()
		if len(q) > 0 {
			t.place(f, h)
		}
		for _, c := range q {
			w.Eval("tickMarqueeAdd(" + c + ")")
		}
	})
	_ = w.Bind("tickMarqueeIdle", func() { t.idle(w, h) })
	_ = w.Bind("tickMarqueeOpen", f.openSession)
	t.mu.Lock()
	t.w, t.hwnd = w, h
	t.mu.Unlock()
	w.Navigate(pageURL(f.srv.port, "view=marquee"))
	w.Run()
	t.mu.Lock()
	t.w, t.hwnd, t.ready, t.opening = nil, 0, false, false
	t.mu.Unlock()
	w.Destroy()
}

// clearFrame 让整个窗口透明：框架扩展到客户区，不要系统背景、圆角和边框
func clearFrame(h uintptr) {
	m := [4]int32{-1, -1, -1, -1}
	pDwmExtendFrame.Call(h, uintptr(unsafe.Pointer(&m)))
	kind, round, border := int32(1), int32(1), uint32(0xFFFFFFFE) // DWMSBT_NONE、DWMWCP_DONOTROUND、DWMWA_COLOR_NONE
	pDwmSetWindowAttribute.Call(h, dwmaSystemBackdrop, uintptr(unsafe.Pointer(&kind)), 4)
	pDwmSetWindowAttribute.Call(h, 33, uintptr(unsafe.Pointer(&round)), 4)
	pDwmSetWindowAttribute.Call(h, 34, uintptr(unsafe.Pointer(&border)), 4)
}

// place 把窗口摆到悬浮窗所在屏幕工作区的上方居中，显示但不激活
func (t *marqueeWin) place(f *floatWin, h uintptr) {
	s := f.scale()
	wa := f.workArea()
	pw, ph := int32(marqueeCSSW*s+0.5), int32(marqueeCSSH*s+0.5)
	if max := (wa.Right - wa.Left) * 92 / 100; pw > max {
		pw = max
	}
	x := wa.Left + (wa.Right-wa.Left-pw)/2
	y := wa.Top + (wa.Bottom-wa.Top)*8/100
	pSetWindowPos.Call(h, ^uintptr(0), uintptr(x), uintptr(y), uintptr(pw), uintptr(ph), swpNoActivate|0x40) // HWND_TOPMOST, SWP_SHOWWINDOW
}

// idle 页面播完队列：藏起来；2 分钟内没新提醒就销毁窗口
func (t *marqueeWin) idle(w webview2.WebView, h uintptr) {
	pShowWindow.Call(h, swHide)
	t.mu.Lock()
	t.gen++
	gen := t.gen
	t.mu.Unlock()
	time.AfterFunc(2*time.Minute, func() {
		t.mu.Lock()
		stale := t.gen == gen && t.w == w && w != nil
		if stale {
			t.ready = false // 之后来的卡先排队，等新窗口建好再显示
		}
		t.mu.Unlock()
		if stale {
			w.Dispatch(w.Terminate)
		}
	})
}

// newClearWebView 与 newGlassWebView 一样把 WebView 底色设成全透明，但不依赖毛玻璃是否开启
func newClearWebView(o webview2.WebViewOptions) webview2.WebView {
	glassEnvMu.Lock()
	defer glassEnvMu.Unlock()
	_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "00000000")
	defer os.Unsetenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR")
	return webview2.NewWithOptions(o)
}
