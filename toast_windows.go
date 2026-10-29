//go:build windows

package main

// 右下角提醒卡：不用 Windows 系统通知。悬浮窗页面发现“勾了一项 / 做完 / 等你”等事件时
// 调 tickToast(卡片 JSON)，这里在悬浮窗所在屏幕的右下角开一个置顶、不抢焦点的小窗口（view=toast）显示，
// 同时让托盘图标闪几下、按设置响提示音。窗口第一次要用时才建；卡片都消失 2 分钟后销毁，省内存。

import (
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

const wsExNoActivate = 0x08000000

type toastWin struct {
	mu      sync.Mutex
	w       webview2.WebView
	hwnd    uintptr
	opening bool
	ready   bool
	queue   []string // 窗口还没加载好时先攒着
	gen     int      // 每次显示 / 隐藏加一，过期的销毁计时器据此作废
}

var theToast = &toastWin{}

// 托盘闪几下：勾一项轻一点，做完、等你多闪几次
var toastBlinks = map[string]int{"item": 2, "done": 3, "failed": 3, "waiting": 4, "blocked": 4}

// toast 收下一张卡片。卡片来自自己的页面，但仍按不可信输入处理：必须是 JSON 对象
func (f *floatWin) toast(card string) {
	var c struct {
		Kind string `json:"kind"`
	}
	if !strings.HasPrefix(strings.TrimSpace(card), "{") || json.Unmarshal([]byte(card), &c) != nil {
		return
	}
	if f.dnd() || f.fsHidden.Load() { // 勿扰、全屏时什么都不打扰
		return
	}
	go f.playNotice()
	n := toastBlinks[c.Kind]
	if n == 0 {
		n = 2
	}
	select {
	case trayFlash <- n:
	default:
	}
	t := theToast
	t.mu.Lock()
	if t.w != nil && t.ready {
		w := t.w
		t.mu.Unlock()
		w.Dispatch(func() { w.Eval("window.tickToastAdd && tickToastAdd(" + card + ")") })
		return
	}
	t.queue = append(t.queue, card)
	if t.opening {
		t.mu.Unlock()
		return
	}
	t.opening = true
	t.mu.Unlock()
	go t.open(f)
}

// open 在自己的线程上建窗口并跑消息循环，直到被销毁
func (t *toastWin) open(f *floatWin) {
	runtime.LockOSThread()
	w := newGlassWebView(webview2.WebViewOptions{
		DataPath:      f.dataPath, // 和悬浮窗共用一份浏览器进程
		WindowOptions: webview2.WindowOptions{Title: "Tick 提醒", Width: 340, Height: 80},
	})
	if w == nil {
		log.Printf("提醒卡窗口创建失败")
		t.mu.Lock()
		t.opening, t.queue = false, nil
		t.mu.Unlock()
		return
	}
	h := uintptr(w.Window())
	pShowWindow.Call(h, swHide)
	pSetWindowLongPtr.Call(h, uintptr(gwlStyleU()), wsPopup|wsClipChildren)
	pSetWindowLongPtr.Call(h, uintptr(gwlExStyleU()), wsExToolWindow|wsExTopmost|wsExNoActivate)
	dark, round := int32(1), int32(2)
	pDwmSetWindowAttribute.Call(h, 20, uintptr(unsafe.Pointer(&dark)), 4)
	pDwmSetWindowAttribute.Call(h, 33, uintptr(unsafe.Pointer(&round)), 4)
	border := uint32(0x00332F2E)
	pDwmSetWindowAttribute.Call(h, 34, uintptr(unsafe.Pointer(&border)), 4)
	applyGlass(h, true)
	_ = w.Bind("tickToastGlass", func() bool { return glassOn })
	_ = w.Bind("tickToastReady", func() {
		t.mu.Lock()
		t.ready, t.opening = true, false
		q := t.queue
		t.queue = nil
		t.mu.Unlock()
		for _, c := range q {
			w.Eval("tickToastAdd(" + c + ")")
		}
	})
	_ = w.Bind("tickToastFit", func(cw, ch float64) { t.fit(f, cw, ch) })
	_ = w.Bind("tickToastOpen", func(sid string) {
		select { // 用户看到了，托盘别再闪
		case trayFlash <- 0:
		default:
		}
		f.dispatch(func() {
			f.summon()
			if strings.HasPrefix(sid, "s_") {
				f.w.Eval(fmt.Sprintf("window.tickFocus && tickFocus(%q)", sid))
			}
		})
	})
	t.mu.Lock()
	t.w, t.hwnd = w, h
	t.mu.Unlock()
	w.Navigate(pageURL(f.srv.port, "view=toast"))
	w.Run()
	t.mu.Lock()
	t.w, t.hwnd, t.ready, t.opening = nil, 0, false, false
	t.mu.Unlock()
	w.Destroy()
}

// fit 页面报告卡片摞的尺寸（CSS 像素）。高度 0 = 没有卡了：藏起来，2 分钟内没新卡就销毁窗口
func (t *toastWin) fit(f *floatWin, cw, ch float64) {
	t.mu.Lock()
	t.gen++
	gen, h, w := t.gen, t.hwnd, t.w
	t.mu.Unlock()
	if ch <= 0 || cw <= 0 {
		pShowWindow.Call(h, swHide)
		time.AfterFunc(2*time.Minute, func() {
			t.mu.Lock()
			stale := t.gen == gen && t.w == w && w != nil
			if stale {
				t.ready = false // 之后来的卡片先排队，等新窗口建好再显示，不会送进正在关的窗口
			}
			t.mu.Unlock()
			if stale {
				w.Dispatch(w.Terminate)
			}
		})
		return
	}
	// 贴在悬浮窗所在屏幕工作区的右下角（任务栏上方），新卡往上长
	s := f.scale()
	pw, ph := int32(cw*s+0.5), int32(ch*s+0.5)
	m := int32(12*s + 0.5)
	wa := f.workArea()
	pSetWindowPos.Call(h, ^uintptr(0), uintptr(wa.Right-pw-m), uintptr(wa.Bottom-ph-m), uintptr(pw), uintptr(ph), swpNoActivate|0x40) // HWND_TOPMOST, SWP_SHOWWINDOW
}
