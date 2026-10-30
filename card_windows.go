//go:build windows

package main

// 长条悬停详情卡：贴边收成长条后，鼠标停在某一段上，在鼠标旁边（长条朝屏幕里的一侧）弹出这个任务的详情：
// 标题、状态、具体子步骤（view=card）。位置按鼠标在长条上的位置摆：上 / 下贴时水平居中在鼠标处，左 / 右贴时垂直居中。
// 卡片不抢焦点、不处理点击（只看）；鼠标离开长条就收起。
// 窗口第一次要用时才建（建好前先攒着内容）；收起 2 分钟没再用就销毁，省一个浏览器渲染进程。

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

type cardWin struct {
	mu      sync.Mutex
	f       *floatWin
	w       webview2.WebView
	hwnd    uintptr
	opening bool
	ready   bool
	want    bool      // 现在应该显示（鼠标还在长条上）；收起后迟到的页面回调据此不再显示
	shown   bool      // 窗口已经摆好显示着
	pinned  bool      // 右键钉住：鼠标离开也不收，右键同一段或点卡片才关
	keep    time.Time // 快捷键弹出的卡片：鼠标不在长条上也先留到这个时间
	watch_  bool      // 盯光标的协程在跑
	pending string    // 窗口建好前要显示的内容
	sid     string
	edge    string
	pt      point   // 鼠标在长条上的屏幕位置
	strip   rect    // 长条窗口的屏幕位置
	scale   float64 // 长条所在屏幕的缩放（页面报的是 CSS 像素）
	pw, ph  int32   // 页面报的卡片尺寸（物理像素）
	gen     int     // 每次显示 / 收起加一，过期的销毁计时器据此作废
}

var theCard = &cardWin{}

var cardColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// show 页面（长条）调：鼠标在 sid 这一段上，along 是鼠标沿长条方向的位置（长条窗口内的 CSS 像素）。
// holdMs > 0：快捷键切到这个任务时弹出，鼠标不在长条上也先留这么久
func (c *cardWin) show(f *floatWin, sid string, along float64, color string, holdMs float64) {
	edge := f.dockEdge()
	if edge == "" || !f.tucked.Load() || !strings.HasPrefix(sid, "s_") {
		return
	}
	if !cardColor.MatchString(color) {
		color = "#4CB782"
	}
	s, err := f.srv.store.Session(sid)
	if err != nil {
		return
	}
	data, err := json.Marshal(map[string]any{"session": s, "color": color})
	if err != nil {
		return
	}
	r, sc := f.windowRect(), f.scale()
	a := int32(along*sc + 0.5)
	pt := point{(r.Left + r.Right) / 2, r.Top + a}
	if edge == "top" || edge == "bottom" {
		pt = point{r.Left + a, (r.Top + r.Bottom) / 2}
	}
	c.mu.Lock()
	c.f = f
	c.want, c.edge, c.pt, c.strip, c.scale = true, edge, pt, r, sc
	c.keep = time.Time{}
	if holdMs > 0 {
		c.keep = time.Now().Add(time.Duration(holdMs) * time.Millisecond)
	}
	same := c.shown && c.sid == sid
	c.sid = sid
	c.gen++
	if c.w != nil && c.ready {
		w := c.w
		c.mu.Unlock()
		if same { // 还是这一段，只是鼠标挪了：不用重画，换个位置
			c.place()
			return
		}
		w.Dispatch(func() { w.Eval("window.tickCardShow && tickCardShow(" + string(data) + ")") })
		return
	}
	c.pending = string(data)
	if c.opening {
		c.mu.Unlock()
		return
	}
	c.opening = true
	c.mu.Unlock()
	go c.create(f)
}

// create 在自己的线程上建窗口并跑消息循环，直到被销毁
func (c *cardWin) create(f *floatWin) {
	runtime.LockOSThread()
	w := newGlassWebView(webview2.WebViewOptions{
		DataPath:      f.dataPath, // 和悬浮窗共用一份浏览器进程
		WindowOptions: webview2.WindowOptions{Title: "Tick Card", Width: 300, Height: 200},
	})
	if w == nil {
		log.Printf("长条详情卡窗口创建失败")
		c.mu.Lock()
		c.opening, c.pending = false, ""
		c.mu.Unlock()
		return
	}
	h := uintptr(w.Window())
	pShowWindow.Call(h, swHide)
	pSetWindowLongPtr.Call(h, uintptr(gwlStyleU()), wsPopup|wsClipChildren)
	pSetWindowLongPtr.Call(h, uintptr(gwlExStyleU()), wsExToolWindow|wsExTopmost|wsExNoActivate)
	round, border := int32(2), uint32(0x00332F2E)
	pDwmSetWindowAttribute.Call(h, 33, uintptr(unsafe.Pointer(&round)), 4)
	pDwmSetWindowAttribute.Call(h, 34, uintptr(unsafe.Pointer(&border)), 4)
	setDarkFrame(h, true)
	applyGlass(h, true)
	_ = w.Bind("tickCardGlass", func() bool { return glassOn })
	_ = w.Bind("tickCardReady", func() {
		c.mu.Lock()
		c.ready, c.opening = true, false
		d := c.pending
		c.pending = ""
		c.mu.Unlock()
		if d != "" {
			w.Eval("tickCardShow(" + d + ")")
		} else {
			c.idleClose(0, w) // 提前建好、还没人悬停过：没用上也别一直占着
		}
	})
	_ = w.Bind("tickCardFit", func(cw, ch float64) { c.fit(cw, ch) })
	// 点卡片：关掉（钉住的卡片就靠这个关）
	_ = w.Bind("tickCardDismiss", func() { c.dismiss() })
	c.mu.Lock()
	c.w, c.hwnd = w, h
	c.mu.Unlock()
	w.Navigate(pageURL(f.srv.port, "view=card"))
	w.Run()
	c.mu.Lock()
	c.w, c.hwnd, c.ready, c.opening, c.shown = nil, 0, false, false, false
	c.mu.Unlock()
	w.Destroy()
}

// fit 页面排好版后报卡片尺寸（CSS 像素），按当前鼠标位置摆好并显示
func (c *cardWin) fit(cw, ch float64) {
	c.mu.Lock()
	sc := c.scale
	if sc <= 0 {
		sc = 1
	}
	c.pw, c.ph = int32(cw*sc+0.5), int32(ch*sc+0.5)
	want := c.want
	c.mu.Unlock()
	if want {
		c.place()
	}
}

// place 摆在鼠标旁边：长条朝屏幕里的一侧，沿长条方向以鼠标为中心，贴着工作区时往里挪
func (c *cardWin) place() {
	c.mu.Lock()
	h, pw, ph, edge, pt, sr, sc, want := c.hwnd, c.pw, c.ph, c.edge, c.pt, c.strip, c.scale, c.want
	c.mu.Unlock()
	if h == 0 || pw <= 0 || ph <= 0 || !want {
		return
	}
	mon, _, _ := pMonitorFromPoint.Call(uintptr(uint32(pt.X))|uintptr(uint32(pt.Y))<<32, 2)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	wa := mi.Work
	gap, m := int32(10*sc+0.5), int32(8*sc+0.5)
	var x, y int32
	switch edge {
	case "top":
		x, y = pt.X-pw/2, sr.Bottom+gap
	case "bottom":
		x, y = pt.X-pw/2, sr.Top-ph-gap
	case "left":
		x, y = sr.Right+gap, pt.Y-ph/2
	default: // right
		x, y = sr.Left-pw-gap, pt.Y-ph/2
	}
	x = max(wa.Left+m, min(x, wa.Right-pw-m))
	y = max(wa.Top+m, min(y, wa.Bottom-ph-m))
	pSetWindowPos.Call(h, ^uintptr(0), uintptr(x), uintptr(y), uintptr(pw), uintptr(ph), swpNoActivate|0x40) // HWND_TOPMOST, SWP_SHOWWINDOW
	c.mu.Lock()
	c.shown = true
	start := !c.watch_
	c.watch_ = true
	c.mu.Unlock()
	if start {
		go c.watch()
	}
}

// watch 盯着光标：鼠标不在长条上（也不在钉住的卡片上）超过 150ms 就收起。
// 页面的 pointerleave 是主要的收起途径，这里兜底：划得很快、窗口只有 6px 高时页面可能收不到离开事件
func (c *cardWin) watch() {
	out := time.Time{}
	for {
		time.Sleep(80 * time.Millisecond)
		c.mu.Lock()
		alive, pinned, keep, sr, sc, f := c.shown, c.pinned, c.keep, c.strip, c.scale, c.f
		if !alive {
			c.watch_ = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		if pinned || time.Now().Before(keep) {
			out = time.Time{}
			continue
		}
		var cur point
		pGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
		m := int32(4*sc + 0.5)
		if cur.X >= sr.Left-m && cur.X < sr.Right+m && cur.Y >= sr.Top-m && cur.Y < sr.Bottom+m {
			out = time.Time{}
			continue
		}
		if out.IsZero() {
			out = time.Now()
		} else if time.Since(out) > 150*time.Millisecond {
			c.mu.Lock()
			c.watch_ = false
			c.mu.Unlock()
			c.gone(f)
			return
		}
	}
}

// pin 右键钉住 / 取消钉住；卡片标题旁跟着显示 / 去掉图钉
func (c *cardWin) pin(on bool) {
	c.mu.Lock()
	c.pinned = on
	w, ready := c.w, c.ready
	c.mu.Unlock()
	if w != nil && ready {
		w.Dispatch(func() { w.Eval(fmt.Sprintf("window.tickCardPinned && tickCardPinned(%t)", on)) })
	}
}

// dismiss 用户点了卡片：关掉并告诉长条页面（它记着有没有钉住）
func (c *cardWin) dismiss() {
	c.mu.Lock()
	f := c.f
	c.mu.Unlock()
	c.gone(f)
}

// gone 收起卡片，并让长条页面把自己记的状态（正在显示哪一段、钉住与否）清掉
func (c *cardWin) gone(f *floatWin) {
	c.hide()
	if f != nil {
		f.dispatch(func() { f.w.Eval("window.tickCardGone && tickCardGone()") })
	}
}

// hide 鼠标离开长条（或长条展开了）：收起；2 分钟没再用就销毁窗口
func (c *cardWin) hide() {
	c.mu.Lock()
	h, w, was := c.hwnd, c.w, c.shown
	c.want, c.shown, c.pinned, c.sid, c.pending = false, false, false, "", ""
	c.gen++
	gen := c.gen
	c.mu.Unlock()
	if h == 0 || w == nil || !was {
		return
	}
	w.Dispatch(func() { w.Eval("window.tickCardHidden && tickCardHidden()") })
	pShowWindow.Call(h, swHide)
	c.idleClose(gen, w)
}

// idleClose 收起 2 分钟没再用就销毁窗口，省一个浏览器渲染进程；长条还收在边上时留着（下次悬停就不用等重建）
func (c *cardWin) idleClose(gen int, w webview2.WebView) {
	time.AfterFunc(2*time.Minute, func() {
		c.mu.Lock()
		stale := c.gen == gen && c.w == w && !c.shown
		f := c.f
		c.mu.Unlock()
		if !stale {
			return
		}
		if f != nil && f.stripLook.Load() {
			c.idleClose(gen, w)
			return
		}
		c.mu.Lock()
		c.ready = false // 之后来的内容先攒着，等新窗口建好再显示，不会送进正在关的窗口
		c.mu.Unlock()
		w.Dispatch(w.Terminate)
	})
}

// warm 收成长条时提前把详情卡窗口建好（隐藏着）：第一次悬停才建要等一两秒
func (c *cardWin) warm(f *floatWin) {
	c.mu.Lock()
	if c.w != nil || c.opening {
		c.mu.Unlock()
		return
	}
	c.opening, c.f = true, f
	c.mu.Unlock()
	go c.create(f)
}
