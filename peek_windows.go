//go:build windows

package main

// 托盘悬停预览：有没读的通知时，鼠标停在托盘图标上，图标上方弹出一个小窗口（view=peek）列出未读，
// 像微信那样；可以点一条直接打开、全部已读、清空。鼠标离开图标和小窗口一会儿就收起。
// 窗口不抢焦点；第一次要用时才建，2 分钟没用就销毁。
//
// 托盘图标是 fyne systray 建的，它只把左右键抬起交给我们。这里把它的隐藏窗口子类化，
// 多接一个“鼠标在图标上移动”（老式回调：lParam = WM_MOUSEMOVE），其余消息原样交回去。

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

var (
	pShellNotifyIconGetRect   = shell32.NewProc("Shell_NotifyIconGetRect")
	pFindWindowEx             = user32.NewProc("FindWindowExW")
	pGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
)

const (
	trayCallbackMsg = 0x0400 + 1 // fyne systray 的 wmSystrayMessage（WM_USER+1）
	trayIconID      = 100        // fyne systray 的图标 ID
	wmMouseMove     = 0x0200
)

type peekWin struct {
	mu      sync.Mutex
	w       webview2.WebView
	hwnd    uintptr
	opening bool
	ready   bool
	shown   bool
	pending string // 窗口建好前要显示的内容
	icon    rect   // 弹出时托盘图标的位置，离开判断用
	gen     int
}

var thePeek = &peekWin{}

var (
	trayOrig  uintptr
	trayHWND  uintptr
	hoverOnce sync.Once
	hoverAt   time.Time // 最近一次鼠标在图标上移动
	hoverMu   sync.Mutex
)

// trayProc 只建一次（NewCallback 的槽位不释放）
var trayProc = syscall.NewCallback(func(h, msg, wp, lp uintptr) uintptr {
	if msg == trayCallbackMsg && lp == wmMouseMove {
		hoverMu.Lock()
		first := time.Since(hoverAt) > 400*time.Millisecond
		hoverAt = time.Now()
		hoverMu.Unlock()
		if first {
			select {
			case hoverPing <- struct{}{}:
			default:
			}
		}
	}
	r, _, _ := pCallWindowProc.Call(trayOrig, h, msg, wp, lp)
	return r
})

var hoverPing = make(chan struct{}, 1)

// trayHover 找到 systray 的隐藏窗口挂上钩子，然后等“鼠标停到图标上”的信号
func trayHover(srv *Server, f *floatWin) {
	h := findTrayWindow()
	if h == 0 {
		log.Printf("没找到托盘窗口，悬停预览不可用")
		return
	}
	trayHWND = h
	trayOrig, _, _ = pGetWindowLongPtr.Call(h, uintptr(gwlpWndProcU()))
	pSetWindowLongPtr.Call(h, uintptr(gwlpWndProcU()), trayProc)
	for range hoverPing {
		// 停一下再弹（划过去不弹）；勿扰、没有未读时不弹
		time.Sleep(350 * time.Millisecond)
		hoverMu.Lock()
		still := time.Since(hoverAt) < 450*time.Millisecond
		hoverMu.Unlock()
		if !still || f.dnd() || srv.store.UnreadNotices() == 0 || theMenuVisible() {
			continue
		}
		thePeek.open(srv, f)
	}
}

func findTrayWindow() uintptr {
	cls, _ := syscall.UTF16PtrFromString("SystrayClass")
	pid := uint32(os.Getpid())
	var h uintptr
	for i := 0; i < 50; i++ { // systray 的窗口在 onReady 之前就建好了；保险起见等一会儿
		h = 0
		for {
			h, _, _ = pFindWindowEx.Call(0, h, uintptr(unsafe.Pointer(cls)), 0)
			if h == 0 {
				break
			}
			var p uint32
			pGetWindowThreadProcessID.Call(h, uintptr(unsafe.Pointer(&p)))
			if p == pid {
				return h
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0
}

// iconRect 托盘图标在屏幕上的位置；拿不到（图标在溢出区收起时）就用光标周围一小块
func iconRect() rect {
	id := struct {
		Size uint32
		Wnd  uintptr
		ID   uint32
		GUID [16]byte
	}{Wnd: trayHWND, ID: trayIconID}
	id.Size = uint32(unsafe.Sizeof(id))
	var r rect
	if hr, _, _ := pShellNotifyIconGetRect.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&r))); hr == 0 && r.Right > r.Left {
		return r
	}
	var c point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&c)))
	return rect{c.X - 16, c.Y - 16, c.X + 16, c.Y + 16}
}

// peekData 未读通知（最新在前，最多 8 条）+ 总数，交给页面画
func peekData(srv *Server) string {
	type row struct {
		ID, SessionID, Agent, Kind, Title, Text string
		At                                      int64
	}
	var rows []row
	total := 0
	for _, n := range srv.store.Notices() { // 已经是最新在前
		if n.Read {
			continue
		}
		total++
		if len(rows) < 8 {
			rows = append(rows, row{n.ID, n.SessionID, n.Agent, n.Kind, n.Title, n.Text, n.At})
		}
	}
	b, _ := json.Marshal(map[string]any{"total": total, "items": rows})
	return string(b)
}

func (p *peekWin) open(srv *Server, f *floatWin) {
	data := peekData(srv)
	p.mu.Lock()
	p.icon = iconRect()
	p.gen++
	if p.w != nil && p.ready {
		w := p.w
		p.mu.Unlock()
		w.Dispatch(func() { w.Eval("window.tickPeekShow && tickPeekShow(" + data + ")") })
		return
	}
	p.pending = data
	if p.opening {
		p.mu.Unlock()
		return
	}
	p.opening = true
	p.mu.Unlock()
	go p.create(srv, f)
}

func (p *peekWin) create(srv *Server, f *floatWin) {
	runtime.LockOSThread()
	w := newGlassWebView(webview2.WebViewOptions{
		DataPath:      f.dataPath,
		WindowOptions: webview2.WindowOptions{Title: "Tick 未读", Width: 320, Height: 200},
	})
	if w == nil {
		log.Printf("托盘预览窗口创建失败")
		p.mu.Lock()
		p.opening = false
		p.mu.Unlock()
		return
	}
	h := uintptr(w.Window())
	pShowWindow.Call(h, swHide)
	pSetWindowLongPtr.Call(h, uintptr(gwlStyleU()), wsPopup|wsClipChildren)
	pSetWindowLongPtr.Call(h, uintptr(gwlExStyleU()), wsExToolWindow|wsExTopmost|wsExNoActivate)
	round := int32(2)
	pDwmSetWindowAttribute.Call(h, 33, uintptr(unsafe.Pointer(&round)), 4)
	border := uint32(0x00332F2E)
	pDwmSetWindowAttribute.Call(h, 34, uintptr(unsafe.Pointer(&border)), 4)
	setDarkFrame(h, true)
	applyGlass(h, true)
	_ = w.Bind("tickPeekReady", func() {
		p.mu.Lock()
		p.ready, p.opening = true, false
		d := p.pending
		p.pending = ""
		p.mu.Unlock()
		if d != "" {
			w.Eval("tickPeekShow(" + d + ")")
		}
	})
	_ = w.Bind("tickPeekGlass", func() bool { return glassOn })
	_ = w.Bind("tickPeekFit", func(cw, ch float64) { p.fit(cw, ch) })
	_ = w.Bind("tickPeekHide", func() { p.hide() })
	// 点一条：叫出悬浮窗并定位到那个会话
	_ = w.Bind("tickPeekOpen", func(sid string) {
		p.hide()
		f.dispatch(func() {
			f.summon()
			if strings.HasPrefix(sid, "s_") {
				f.w.Eval(fmt.Sprintf("window.tickFocus && tickFocus(%q)", sid))
			}
		})
	})
	p.mu.Lock()
	p.w, p.hwnd = w, h
	p.mu.Unlock()
	w.Navigate(pageURL(srv.port, "view=peek"))
	w.Run()
	p.mu.Lock()
	p.w, p.hwnd, p.ready, p.opening, p.shown = nil, 0, false, false, false
	p.mu.Unlock()
	w.Destroy()
}

// fit 页面排好版后报尺寸：摆在托盘图标正上方（任务栏在上面时放下方），然后开始盯鼠标
func (p *peekWin) fit(cw, ch float64) {
	p.mu.Lock()
	h, ic := p.hwnd, p.icon
	p.mu.Unlock()
	if cw <= 0 || ch <= 0 || h == 0 {
		return
	}
	c := point{(ic.Left + ic.Right) / 2, (ic.Top + ic.Bottom) / 2}
	mon, _, _ := pMonitorFromPoint.Call(uintptr(uint32(c.X))|uintptr(uint32(c.Y))<<32, 2)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	dpi, _, _ := pGetDpiForWindow.Call(h)
	s := 1.0
	if dpi > 0 {
		s = float64(dpi) / 96
	}
	pw, ph := int32(cw*s+0.5), int32(ch*s+0.5)
	gap := int32(8*s + 0.5)
	wa := mi.Work
	x := min(max(c.X-pw/2, wa.Left+gap), wa.Right-pw-gap)
	y := ic.Top - ph - gap
	if y < wa.Top+gap {
		y = ic.Bottom + gap
	}
	pSetWindowPos.Call(h, ^uintptr(0), uintptr(x), uintptr(y), uintptr(pw), uintptr(ph), swpNoActivate|0x40) // HWND_TOPMOST, SWP_SHOWWINDOW
	p.mu.Lock()
	first := !p.shown
	p.shown = true
	gen := p.gen
	p.mu.Unlock()
	if first {
		go p.watch(h, gen)
	}
}

// watch 鼠标离开图标和小窗口（中间的空隙也算在内）超过 350ms 就收起
func (p *peekWin) watch(h uintptr, gen int) {
	out := time.Time{}
	for {
		time.Sleep(80 * time.Millisecond)
		p.mu.Lock()
		alive, ic := p.hwnd == h && p.shown, p.icon
		p.mu.Unlock()
		if !alive {
			return
		}
		var c point
		var wr rect
		pGetCursorPos.Call(uintptr(unsafe.Pointer(&c)))
		pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr)))
		// 图标和窗口之间的那条缝也算“还在”，鼠标从图标往上挪过去时不会中途收起
		zone := rect{min(wr.Left, ic.Left) - 6, min(wr.Top, ic.Top) - 6, max(wr.Right, ic.Right) + 6, max(wr.Bottom, ic.Bottom) + 6}
		inWin := c.X >= wr.Left && c.X < wr.Right && c.Y >= wr.Top && c.Y < wr.Bottom
		inIcon := c.X >= ic.Left-4 && c.X < ic.Right+4 && c.Y >= ic.Top-4 && c.Y < ic.Bottom+4
		inGap := c.X >= max(wr.Left, ic.Left-40) && c.X < min(wr.Right, ic.Right+40) && c.Y >= zone.Top && c.Y < zone.Bottom
		if inWin || inIcon || inGap {
			out = time.Time{}
			continue
		}
		if out.IsZero() {
			out = time.Now()
		} else if time.Since(out) > 350*time.Millisecond {
			p.hide()
			return
		}
	}
}

func (p *peekWin) hide() {
	p.mu.Lock()
	h, w := p.hwnd, p.w
	was := p.shown
	p.shown = false
	p.gen++
	gen := p.gen
	p.mu.Unlock()
	if h == 0 || !was {
		return
	}
	w.Dispatch(func() { w.Eval("window.tickPeekHidden && tickPeekHidden()") })
	pShowWindow.Call(h, swHide)
	time.AfterFunc(2*time.Minute, func() {
		p.mu.Lock()
		stale := p.gen == gen && p.w == w && w != nil && !p.shown
		if stale {
			p.ready = false
		}
		p.mu.Unlock()
		if stale {
			w.Dispatch(w.Terminate)
		}
	})
}

// theMenuVisible 右键菜单开着时不弹预览，免得两个窗口叠在一起
func theMenuVisible() bool {
	theMenu.mu.Lock()
	h := theMenu.hwnd
	theMenu.mu.Unlock()
	if h == 0 {
		return false
	}
	v, _, _ := pIsWindowVisible.Call(h)
	return v != 0
}
