//go:build windows

package main

// 托盘右键菜单：现代化的宫格弹窗（view=menu），代替系统的文字菜单。
// 所有菜单动作集中在 tray.act，弹窗和系统菜单（弹窗建不出来时的后备）共用。
// 弹窗第一次右键时才建；失去焦点就收起；10 分钟没用就销毁，省内存。

import (
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/systray"
	webview2 "github.com/jchv/go-webview2"
)

type tray struct {
	f       *floatWin
	srv     *Server
	up      *updater
	refresh func() // 同步系统菜单里的勾选状态
}

// trayState 弹窗要显示的一切：开关状态、版本、会话统计
type trayState struct {
	Visible           bool         `json:"visible"`
	Pinned            bool         `json:"pinned"`
	AutoHide          bool         `json:"autoHide"`
	FsHide            bool         `json:"fsHide"`
	Dnd               bool         `json:"dnd"`
	DndUntil          int64        `json:"dndUntil"`
	Autostart         bool         `json:"autostart"`
	AutoUpdate        bool         `json:"autoUpdate"`
	AutoArchive       bool         `json:"autoArchive"`
	TrayFlash         bool         `json:"trayFlash"`
	NotificationBadge bool         `json:"notificationBadge"`
	Sound             string       `json:"sound"`
	Sounds            []soundOpt   `json:"sounds"`
	Theme             colorTheme   `json:"theme"`
	Lang              string       `json:"lang"` // 用户选的语言，"" = 跟系统
	Strip             bool         `json:"strip"`
	Screen            bool         `json:"screen"` // 大屏窗口开着
	ToastLen          string       `json:"toastLen"`
	Themes            []colorTheme `json:"themes"`
	Version           string       `json:"version"`
	Pending           string       `json:"pending"`
	Hotkey            string       `json:"hotkey"`
	Running           int          `json:"running"`
	Waiting           int          `json:"waiting"`
	Done              int          `json:"done"`
	Total             int          `json:"total"`
	Glass             bool         `json:"glass"`

	Restart       bool   `json:"restart,omitempty"`
	EffectiveLang string `json:"effectiveLang"`
}

func (t *tray) state() trayState {
	f := t.f
	f.mu.Lock()
	st := trayState{Pinned: f.set.Pinned, AutoHide: !f.set.NoAutoHide, FsHide: !f.set.NoFullscreenHide, DndUntil: f.set.DndUntil,
		AutoUpdate: !f.set.NoAutoUpdate, AutoArchive: f.set.AutoArchive, TrayFlash: !f.set.NoTrayFlash, NotificationBadge: !f.set.NoBadge, Sound: f.set.Sound, Lang: f.set.Lang, Strip: f.set.Strip, ToastLen: f.set.ToastLen}
	f.mu.Unlock()
	st.Visible = f.visible() && !f.tucked.Load()
	st.Screen = screenOn()
	st.Dnd = f.dnd()
	st.Theme, st.Themes = f.theme(), themes
	st.Autostart = autostartEnabled()
	st.Sounds = f.sounds()
	st.Version, st.Pending, st.Hotkey, st.Glass = version, t.up.pending(), f.hotkey, glassOn
	st.EffectiveLang = curLang()
	for _, s := range t.srv.store.Sessions(false) {
		active := isWaiting(s.Status) || s.Status == "running" || s.Status == "planning"
		if !active {
			continue
		}
		if s.Status == "waiting" {
			st.Waiting++
		} else if s.Status != "blocked" {
			st.Running++
		}
		for _, it := range s.Items {
			st.Total++
			if it.Status == "done" || it.Status == "skipped" {
				st.Done++
			}
		}
	}
	return st
}

// act 执行一个菜单动作。on 是开关的目标状态，val 是附带的值（比如提示音 ID）
func (t *tray) act(name string, on bool, val string) {
	f, srv := t.f, t.srv
	setFlag := func(p *bool, v bool) {
		f.mu.Lock()
		*p = v
		f.mu.Unlock()
		f.saveSettings()
	}
	switch name {
	case "float":
		f.toggle()
	case "pin":
		f.dispatch(func() { f.setPinned(on) })
	case "autohide":
		setFlag(&f.set.NoAutoHide, !on)
		if !on && f.tucked.Load() {
			f.inDock(func() { f.setTucked(false) })
		}
	case "fs":
		setFlag(&f.set.NoFullscreenHide, !on)
	case "dnd", "autostart", "toastlen", "sound", "lang", "theme", "autoarchive", "trayflash", "notificationbadge":
		restart, err := t.applySetting(name, on, val)
		if err != nil {
			log.Printf("设置 %s 失败：%v", name, err)
		} else if restart {
			go t.up.restartWhenIdle(true)
		}
	case "autoupdate":
		setFlag(&f.set.NoAutoUpdate, !on)
	case "strip":
		setFlag(&f.set.Strip, on)
		f.inDock(func() { f.applyStripLook(); f.notifyDock() }) // 已经收着时马上在长条和小环之间换
		f.pushHostState()
	case "screen":
		f.setScreen(on)
	case "update":
		if t.up.pending() != "" {
			go t.up.restartWhenIdle(true)
		} else {
			go t.up.check(true)
		}
	case "panel":
		openPanel(srv, f, "")
	case "browser":
		_ = openExternal(fmt.Sprintf("http://127.0.0.1:%d/", srv.port))
	case "mcp":
		copyText(fmt.Sprintf("http://127.0.0.1:%d/mcp", srv.port))
	case "sounddir":
		f.openSoundDir()
	case "quit":
		f.dispatch(func() {
			f.saveSettings()
			f.w.Terminate()
		})
		systray.Quit()
		return
	}
	if t.refresh != nil {
		t.refresh()
	}
}

// ---------- 弹窗 ----------

type menuWin struct {
	mu        sync.Mutex
	w         webview2.WebView
	hwnd      uintptr
	orig      uintptr
	opening   bool
	ready     bool
	wantShow  bool // 下一次页面报尺寸时显示出来（先排好版再出现，不闪）
	anchor    point
	gen       int
	failed    bool
	parent    *menuWin // nil for the main menu; settings is its owned popup
	subY      float64
	wantFocus bool
}

var theMenu = &menuWin{}
var theSubMenu = &menuWin{parent: theMenu}

// menuProc 只建一次：syscall.NewCallback 的槽位永不释放，窗口每次重建都新建会慢慢耗尽
var menuProc = syscall.NewCallback(func(hh, msg, wp, lp uintptr) uintptr {
	m := theMenu
	theSubMenu.mu.Lock()
	subH := theSubMenu.hwnd
	theSubMenu.mu.Unlock()
	if hh == subH {
		m = theSubMenu
	}
	if msg == wmActivate && wp&0xFFFF == waInactive {
		theMenu.mu.Lock()
		mainH := theMenu.hwnd
		theMenu.mu.Unlock()
		inside := lp != 0 && (lp == mainH || lp == subH)
		if lp == 0 {
			foreground, _, _ := pGetForegroundWindow.Call()
			inside = (hh == mainH && subH != 0 && foreground == subH) ||
				(hh == subH && mainH != 0 && foreground == mainH)
		}
		if !inside {
			theMenu.hide()
		}
	}
	if msg == 0x100 && wp == 0x1B { // WM_KEYDOWN + VK_ESCAPE：焦点还在宿主窗口上、没进页面时也能关
		theMenu.closeLevel()
		return 0
	}
	if msg == wmClose {
		theMenu.hide()
		return 0
	}
	r, _, _ := pCallWindowProc.Call(m.orig, hh, msg, wp, lp)
	return r
})

var (
	pMonitorFromPoint = user32.NewProc("MonitorFromPoint")
	pSetFocus         = user32.NewProc("SetFocus")
	pGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
)

const (
	wmActivate = 0x06
	waInactive = 0
)

// open 右键托盘：在光标附近弹出。返回 false 表示弹窗建不出来，调用方改用系统菜单
func (m *menuWin) open(t *tray) bool {
	m.closeSub(false)
	var c point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&c)))
	m.mu.Lock()
	if m.failed {
		m.mu.Unlock()
		return false
	}
	m.anchor, m.wantShow = c, true
	m.gen++
	if m.w != nil && m.ready {
		w := m.w
		m.mu.Unlock()
		b, _ := json.Marshal(t.state())
		w.Dispatch(func() { w.Eval("window.tickMenuOpen && tickMenuOpen(" + string(b) + ")") })
		return true
	}
	if !m.opening {
		m.opening = true
		go m.create(t)
	}
	m.mu.Unlock()
	return true
}

// openSub keeps the main window fixed. Hover only shows; explicit entry requests focus.
func (m *menuWin) openSub(t *tray, y float64, focus bool) {
	m.mu.Lock()
	mainH, visible := m.hwnd, m.ready
	m.mu.Unlock()
	if v, _, _ := pIsWindowVisible.Call(mainH); !visible || v == 0 {
		return
	}
	sub := theSubMenu
	sub.mu.Lock()
	sub.subY, sub.wantShow = y, true
	sub.wantFocus = sub.wantFocus || focus
	sub.gen++
	w, ready := sub.w, sub.ready
	if w != nil && ready {
		sub.mu.Unlock()
		b, _ := json.Marshal(t.state())
		w.Dispatch(func() { w.Eval("tickMenuOpen(" + string(b) + ")") })
	} else {
		if !sub.opening {
			sub.opening = true
			go sub.create(t)
		}
		sub.mu.Unlock()
	}
	m.subState(true, false)
}

func (m *menuWin) subState(open, focus bool) {
	m.mu.Lock()
	w := m.w
	m.mu.Unlock()
	if w != nil {
		w.Dispatch(func() { w.Eval(fmt.Sprintf("window.tickMenuSubState && tickMenuSubState(%t,%t)", open, focus)) })
	}
}

func (m *menuWin) closeSub(focus bool) {
	theSubMenu.hide()
	if focus {
		m.mu.Lock()
		h := m.hwnd
		m.mu.Unlock()
		if v, _, _ := pIsWindowVisible.Call(h); v != 0 {
			pSetForegroundWindow.Call(h)
			m.subState(false, true)
		}
	}
}

func (m *menuWin) closeLevel() {
	theSubMenu.mu.Lock()
	h, pending := theSubMenu.hwnd, theSubMenu.wantShow
	theSubMenu.mu.Unlock()
	if v, _, _ := pIsWindowVisible.Call(h); pending || v != 0 {
		m.closeSub(true)
	} else {
		m.hide()
	}
}

func (m *menuWin) create(t *tray) {
	runtime.LockOSThread()
	defer func() {
		m.mu.Lock()
		m.w, m.hwnd, m.ready, m.opening = nil, 0, false, false
		m.mu.Unlock()
	}()
	if m.parent != nil {
		// The library initializes COM only on its package-init thread. This child
		// runs on its own locked thread and must establish its STA explicitly.
		ole32 := syscall.NewLazyDLL("ole32.dll")
		hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2) // COINIT_APARTMENTTHREADED
		if int32(hr) < 0 {
			log.Printf("创建设置子菜单失败：CoInitializeEx HRESULT=%08x", uint32(hr))
			m.mu.Lock()
			m.wantShow, m.wantFocus = false, false
			m.mu.Unlock()
			m.parent.subState(false, false)
			return
		}
		defer ole32.NewProc("CoUninitialize").Call() // balances both S_OK and S_FALSE
	}
	f := t.f
	title := "Tick 菜单"
	if m.parent != nil {
		title = "Tick 显示与通知"
	}
	o := webview2.WebViewOptions{
		DataPath:      f.dataPath,
		AutoFocus:     true,
		WindowOptions: webview2.WindowOptions{Title: title, Width: 320, Height: 420, Hidden: m.parent != nil},
	}
	w := newGlassWebView(o)

	if w == nil {
		// 这次右键先把悬浮窗叫出来；下次右键时（在托盘线程里）换成系统菜单
		log.Printf("托盘弹窗创建失败，改用系统菜单")
		m.mu.Lock()
		m.opening, m.failed, m.wantShow, m.wantFocus = false, true, false, false
		m.mu.Unlock()
		if m.parent != nil {
			m.parent.subState(false, false)
		} else {
			f.dispatch(f.summon)
		}
		return
	}
	h := uintptr(w.Window())
	pShowWindow.Call(h, swHide)
	if m.parent != nil {
		m.parent.mu.Lock()
		owner := m.parent.hwnd
		m.parent.mu.Unlock()
		pSetWindowLongPtr.Call(h, ^uintptr(7), owner) // GWLP_HWNDPARENT
	}
	m.mu.Lock()
	m.w, m.hwnd = w, h
	m.mu.Unlock()
	pSetWindowLongPtr.Call(h, uintptr(gwlStyleU()), wsPopup|wsClipChildren)
	pSetWindowLongPtr.Call(h, uintptr(gwlExStyleU()), wsExToolWindow|wsExTopmost)
	round := int32(2)
	pDwmSetWindowAttribute.Call(h, 33, uintptr(unsafe.Pointer(&round)), 4)
	border := uint32(0x00332F2E)
	pDwmSetWindowAttribute.Call(h, 34, uintptr(unsafe.Pointer(&border)), 4)
	setDarkFrame(h, true)
	applyGlass(h, true)
	// 失去焦点就收起，像系统菜单一样
	m.orig, _, _ = pGetWindowLongPtr.Call(h, uintptr(gwlpWndProcU()))
	pSetWindowLongPtr.Call(h, uintptr(gwlpWndProcU()), menuProc)
	state := func() string {
		b, _ := json.Marshal(t.state())
		return string(b)
	}
	_ = w.Bind("tickMenuReady", func() {
		m.mu.Lock()
		m.ready, m.opening = true, false
		m.mu.Unlock()
		w.Eval("tickMenuOpen(" + state() + ")")
	})
	_ = w.Bind("tickMenuFit", func(cw, ch float64) { m.fit(cw, ch) })
	_ = w.Bind("tickMenuClose", func() {
		if m.parent != nil {
			m.parent.closeSub(true)
		} else {
			m.closeLevel()
		}
	})
	if m.parent == nil {
		_ = w.Bind("tickMenuSubOpen", func(y float64, focus bool) { m.openSub(t, y, focus) })
		_ = w.Bind("tickMenuSubClose", func(focus bool) { m.closeSub(focus) })
	}
	// 动作执行完回新的状态给页面；会离开菜单的动作（打开面板、退出等）顺手收起
	_ = w.Bind("tickMenuAct", func(name string, on bool, val string) string {
		switch name {
		case "float", "panel", "browser", "mcp", "update", "sounddir", "quit", "lang":
			theMenu.hide()
		}
		t.act(name, on, val)
		if name == "dnd" || name == "sound" { // 这两个会异步推状态，等一下再取
			time.Sleep(60 * time.Millisecond)
		}
		st := state()
		if m.parent != nil {
			m.parent.mu.Lock()
			parentW := m.parent.w
			m.parent.mu.Unlock()
			if parentW != nil {
				parentW.Dispatch(func() { parentW.Eval("window.tickMenuState && tickMenuState(" + st + ")") })
			}
		}
		return st
	})
	view := "view=menu"
	if m.parent != nil {
		view += "&submenu=1"
	}
	if m.parent != nil {
		m.parent.mu.Lock()
		alive := m.parent.ready
		m.parent.mu.Unlock()
		if !alive {
			w.Destroy()
			return
		}
	}
	w.Navigate(pageURL(f.srv.port, view))
	w.Run()
	m.mu.Lock()
	m.ready = false
	m.mu.Unlock()
	if m.parent == nil {
		theSubMenu.hide()
		theSubMenu.mu.Lock()
		subW := theSubMenu.w
		theSubMenu.mu.Unlock()
		if subW != nil {
			subW.Dispatch(subW.Terminate)
		}
	}
	w.Destroy()
}

// menuBounds keeps the whole popup inside the anchor monitor's work area.
// Oversized content scrolls in the page; the native window never covers the taskbar.
func menuBounds(cw, ch, scale float64, c point, wa rect) rect {
	gap := int32(10*scale + 0.5)
	pw := min(int32(cw*scale+0.5), max(1, wa.Right-wa.Left-2*gap))
	ph := min(int32(ch*scale+0.5), max(1, wa.Bottom-wa.Top-2*gap))
	x := min(max(c.X-pw+gap*3, wa.Left+gap), wa.Right-pw-gap)
	y := c.Y - ph - gap
	if y < wa.Top+gap {
		y = c.Y + gap
	}
	y = min(max(y, wa.Top+gap), wa.Bottom-ph-gap)
	return rect{Left: x, Top: y, Right: x + pw, Bottom: y + ph}
}

// submenuBounds prefers the right side and flips left without moving the parent.
func submenuBounds(cw, ch, scale, rowY float64, parent, work rect) rect {
	gap := int32(8*scale + 0.5)
	margin := int32(10*scale + 0.5)
	pw := min(int32(cw*scale+0.5), max(1, work.Right-work.Left-2*margin))
	ph := min(int32(ch*scale+0.5), max(1, work.Bottom-work.Top-2*margin))
	x := parent.Right + gap
	if x+pw > work.Right-margin {
		x = parent.Left - gap - pw
	}
	x = min(max(x, work.Left+margin), work.Right-pw-margin)
	y := min(max(parent.Top+int32(rowY*scale+0.5), work.Top+margin), work.Bottom-ph-margin)
	return rect{Left: x, Top: y, Right: x + pw, Bottom: y + ph}
}

// fit also accepts content changes while visible. Only the opening request shows
// or activates the window; resize callbacks after hide must not bring it back.
func (m *menuWin) fit(cw, ch float64) {
	m.mu.Lock()
	h, show, c, gen, focus, rowY := m.hwnd, m.wantShow, m.anchor, m.gen, m.wantFocus, m.subY
	m.mu.Unlock()
	if h == 0 || cw <= 0 || ch <= 0 {
		return
	}
	if visible, _, _ := pIsWindowVisible.Call(h); !show && visible == 0 {
		return
	}
	var parentRect rect
	if m.parent != nil {
		m.parent.mu.Lock()
		parentH := m.parent.hwnd
		c = m.parent.anchor
		m.parent.mu.Unlock()
		if v, _, _ := pIsWindowVisible.Call(parentH); v == 0 {
			return
		}
		pGetWindowRect.Call(parentH, uintptr(unsafe.Pointer(&parentRect)))
	}
	mon, _, _ := pMonitorFromPoint.Call(uintptr(uint32(c.X))|uintptr(uint32(c.Y))<<32, 2)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ok, _, _ := pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return
	}
	dpi, _, _ := pGetDpiForWindow.Call(h)
	s := 1.0
	if dpi > 0 {
		s = float64(dpi) / 96
	}
	r := menuBounds(cw, ch, s, c, mi.Work)
	if m.parent != nil {
		r = submenuBounds(cw, ch, s, rowY, parentRect, mi.Work)
	}
	m.mu.Lock()
	if m.hwnd != h || m.gen != gen {
		m.mu.Unlock()
		return
	}
	m.wantShow, m.wantFocus = false, false
	m.mu.Unlock()
	flags := uintptr(0x14) // SWP_NOZORDER | SWP_NOACTIVATE
	if show {
		flags = 0x40 // SWP_SHOWWINDOW
		if m.parent != nil && !focus {
			flags |= 0x10
		} // hover must not activate
	} else {
		var current rect
		pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&current)))
		if current == r {
			return
		}
	}
	pSetWindowPos.Call(h, ^uintptr(0), uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), flags)
	if show {
		if m.parent == nil || focus {
			pSetForegroundWindow.Call(h)
			if m.parent != nil {
				m.w.Eval("window.tickMenuFocus && tickMenuFocus()")
			}
		}
		if m.parent == nil {
			go m.watchOutside(h, gen)
		}
	}
}

// watchOutside 后备的“点外面就收起”：系统不给前台权限时（比如别的程序锁着前台），
// 菜单拿不到焦点，也就收不到失焦消息。弹出期间每 60ms 看一下鼠标键：在菜单外面按下就收起
func (m *menuWin) watchOutside(h uintptr, gen int) {
	var held bool
	for {
		time.Sleep(60 * time.Millisecond)
		m.mu.Lock()
		alive := m.gen == gen && m.hwnd == h
		m.mu.Unlock()
		if v, _, _ := pIsWindowVisible.Call(h); !alive || v == 0 {
			return
		}
		l, _, _ := pGetAsyncKeyState.Call(0x01) // VK_LBUTTON
		r, _, _ := pGetAsyncKeyState.Call(0x02) // VK_RBUTTON
		down := l&0x8000 != 0 || r&0x8000 != 0
		if down && !held {
			var c point
			var wr rect
			pGetCursorPos.Call(uintptr(unsafe.Pointer(&c)))
			pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr)))
			inside := c.X >= wr.Left && c.X < wr.Right && c.Y >= wr.Top && c.Y < wr.Bottom
			theSubMenu.mu.Lock()
			subH := theSubMenu.hwnd
			theSubMenu.mu.Unlock()
			if visible, _, _ := pIsWindowVisible.Call(subH); visible != 0 {
				pGetWindowRect.Call(subH, uintptr(unsafe.Pointer(&wr)))
				inside = inside || (c.X >= wr.Left && c.X < wr.Right && c.Y >= wr.Top && c.Y < wr.Bottom)
			}
			if !inside {
				m.hide()
				return
			}
		}
		held = down
	}
}

func (m *menuWin) hide() {
	if m.parent == nil {
		theSubMenu.hide()
	} else {
		m.parent.subState(false, false)
	}
	m.mu.Lock()
	h, w := m.hwnd, m.w
	m.wantShow, m.wantFocus = false, false
	m.gen++
	gen := m.gen
	m.mu.Unlock()
	if h == 0 {
		return
	}
	pShowWindow.Call(h, swHide)
	if w != nil {
		w.Dispatch(func() { w.Eval("window.tickMenuHidden && tickMenuHidden()") })
	}
	time.AfterFunc(10*time.Minute, func() {
		m.mu.Lock()
		stale := m.gen == gen && m.w == w && w != nil
		m.mu.Unlock()
		if stale {
			w.Dispatch(w.Terminate)
		}
	})
}
