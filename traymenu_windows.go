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
	Visible    bool       `json:"visible"`
	Pinned     bool       `json:"pinned"`
	AutoHide   bool       `json:"autoHide"`
	FsHide     bool       `json:"fsHide"`
	Dnd        bool       `json:"dnd"`
	DndUntil   int64      `json:"dndUntil"`
	Autostart  bool       `json:"autostart"`
	AutoUpdate bool       `json:"autoUpdate"`
	Sound      string     `json:"sound"`
	Sounds     []soundOpt `json:"sounds"`
	Version    string     `json:"version"`
	Pending    string     `json:"pending"`
	Hotkey     string     `json:"hotkey"`
	Running    int        `json:"running"`
	Waiting    int        `json:"waiting"`
	Done       int        `json:"done"`
	Total      int        `json:"total"`
	Glass      bool       `json:"glass"`
}

func (t *tray) state() trayState {
	f := t.f
	f.mu.Lock()
	st := trayState{Pinned: f.set.Pinned, AutoHide: !f.set.NoAutoHide, FsHide: !f.set.NoFullscreenHide, DndUntil: f.set.DndUntil,
		AutoUpdate: !f.set.NoAutoUpdate, Sound: f.set.Sound}
	f.mu.Unlock()
	st.Visible = f.visible() && !f.tucked.Load()
	st.Dnd = f.dnd()
	st.Autostart = autostartEnabled()
	st.Sounds = f.sounds()
	st.Version, st.Pending, st.Hotkey, st.Glass = version, t.up.pending(), f.hotkey, glassOn
	for _, s := range t.srv.store.Sessions(false) {
		active := isWaiting(s.Status) || s.Status == "running" || s.Status == "planning"
		if !active {
			continue
		}
		if isWaiting(s.Status) {
			st.Waiting++
		} else {
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
	case "dnd":
		f.setDnd(on)
	case "autostart":
		if err := setAutostart(on); err != nil {
			log.Printf("开机启动设置失败：%v", err)
		}
	case "autoupdate":
		setFlag(&f.set.NoAutoUpdate, !on)
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
	case "sound":
		f.setSound(val) // 自己会推状态、试听
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
	mu       sync.Mutex
	w        webview2.WebView
	hwnd     uintptr
	orig     uintptr
	opening  bool
	ready    bool
	wantShow bool // 下一次页面报尺寸时显示出来（先排好版再出现，不闪）
	anchor   point
	gen      int
	failed   bool
}

var theMenu = &menuWin{}

// menuProc 只建一次：syscall.NewCallback 的槽位永不释放，窗口每次重建都新建会慢慢耗尽
var menuProc = syscall.NewCallback(func(hh, msg, wp, lp uintptr) uintptr {
	m := theMenu
	if msg == wmActivate && wp&0xFFFF == waInactive {
		m.hide()
	}
	if msg == 0x100 && wp == 0x1B { // WM_KEYDOWN + VK_ESCAPE：焦点还在宿主窗口上、没进页面时也能关
		m.hide()
		return 0
	}
	if msg == wmClose {
		m.hide()
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

func (m *menuWin) create(t *tray) {
	runtime.LockOSThread()
	f := t.f
	w := newGlassWebView(webview2.WebViewOptions{
		DataPath:      f.dataPath,
		WindowOptions: webview2.WindowOptions{Title: "Tick 菜单", Width: 320, Height: 420},
	})
	if w == nil {
		// 这次右键先把悬浮窗叫出来；下次右键时（在托盘线程里）换成系统菜单
		log.Printf("托盘弹窗创建失败，改用系统菜单")
		m.mu.Lock()
		m.opening, m.failed = false, true
		m.mu.Unlock()
		f.dispatch(f.summon)
		return
	}
	h := uintptr(w.Window())
	pShowWindow.Call(h, swHide)
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
	_ = w.Bind("tickMenuFit", func(cw, ch float64) { m.fit(f, cw, ch) })
	_ = w.Bind("tickMenuClose", func() { m.hide() })
	// 动作执行完回新的状态给页面；会离开菜单的动作（打开面板、退出等）顺手收起
	_ = w.Bind("tickMenuAct", func(name string, on bool, val string) string {
		switch name {
		case "float", "panel", "browser", "mcp", "update", "sounddir", "quit":
			m.hide()
		}
		t.act(name, on, val)
		if name == "dnd" || name == "sound" { // 这两个会异步推状态，等一下再取
			time.Sleep(60 * time.Millisecond)
		}
		return state()
	})
	m.mu.Lock()
	m.w, m.hwnd = w, h
	m.mu.Unlock()
	w.Navigate(fmt.Sprintf("http://127.0.0.1:%d/?view=menu", f.srv.port))
	w.Run()
	m.mu.Lock()
	m.w, m.hwnd, m.ready, m.opening = nil, 0, false, false
	m.mu.Unlock()
	w.Destroy()
}

// fit 页面排好版后报尺寸：要显示就摆到光标附近（贴着任务栏上方、靠右对齐光标）并拿到焦点
func (m *menuWin) fit(f *floatWin, cw, ch float64) {
	m.mu.Lock()
	h, show, c := m.hwnd, m.wantShow, m.anchor
	m.wantShow = false
	m.mu.Unlock()
	if !show || cw <= 0 || ch <= 0 {
		return
	}
	mon, _, _ := pMonitorFromPoint.Call(uintptr(uint32(c.X))|uintptr(uint32(c.Y))<<32, 2)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	dpi, _, _ := pGetDpiForWindow.Call(h)
	s := 1.0
	if dpi > 0 {
		s = float64(dpi) / 96
	}
	pw, ph := int32(cw*s+0.5), int32(ch*s+0.5)
	gap := int32(10*s + 0.5)
	wa := mi.Work
	x := min(max(c.X-pw+gap*3, wa.Left+gap), wa.Right-pw-gap)
	y := c.Y - ph - gap
	if y < wa.Top+gap { // 任务栏在上面：往下弹
		y = c.Y + gap
	}
	y = min(max(y, wa.Top+gap), wa.Bottom-ph-gap)
	pSetWindowPos.Call(h, ^uintptr(0), uintptr(x), uintptr(y), uintptr(pw), uintptr(ph), 0x40) // HWND_TOPMOST, SWP_SHOWWINDOW
	pSetForegroundWindow.Call(h)
	pSetFocus.Call(h)
	m.mu.Lock()
	gen := m.gen
	m.mu.Unlock()
	go m.watchOutside(h, gen)
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
			if c.X < wr.Left || c.X >= wr.Right || c.Y < wr.Top || c.Y >= wr.Bottom {
				m.hide()
				return
			}
		}
		held = down
	}
}

func (m *menuWin) hide() {
	m.mu.Lock()
	h, w := m.hwnd, m.w
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
