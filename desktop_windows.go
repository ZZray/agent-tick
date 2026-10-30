//go:build windows

package main

// Windows 桌面端：托盘图标、原生置顶悬浮窗（WebView2，无边框、圆角、跟随内容调高度）、系统通知。

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/systray"
	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const desktopSupported = true

func init() { runtime.LockOSThread() } // 悬浮窗的消息循环必须在创建它的线程上

var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	dwmapi                 = windows.NewLazySystemDLL("dwmapi.dll")
	pSetWindowLongPtr      = user32.NewProc("SetWindowLongPtrW")
	pGetWindowLongPtr      = user32.NewProc("GetWindowLongPtrW")
	pCallWindowProc        = user32.NewProc("CallWindowProcW")
	pSetWindowPos          = user32.NewProc("SetWindowPos")
	pShowWindow            = user32.NewProc("ShowWindow")
	pIsWindowVisible       = user32.NewProc("IsWindowVisible")
	pGetWindowRect         = user32.NewProc("GetWindowRect")
	pReleaseCapture        = user32.NewProc("ReleaseCapture")
	pPostMessage           = user32.NewProc("PostMessageW")
	pSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	pMessageBox            = user32.NewProc("MessageBoxW")
	pSetDpiAwareness       = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForWindow       = user32.NewProc("GetDpiForWindow")
	pMonitorFromWindow     = user32.NewProc("MonitorFromWindow")
	pGetMonitorInfo        = user32.NewProc("GetMonitorInfoW")
	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	pGetCursorPos          = user32.NewProc("GetCursorPos")
	pRegisterHotKey        = user32.NewProc("RegisterHotKey")
)

const (
	gwlStyle, gwlExStyle, gwlpWndProc = -16, -20, -4
	wsPopup, wsClipChildren           = 0x80000000, 0x02000000
	wsExToolWindow, wsExTopmost       = 0x80, 0x8
	swpNoSize, swpNoMove, swpNoZOrder = 0x1, 0x2, 0x4
	swpNoActivate, swpFrameChanged    = 0x10, 0x20
	wmClose, wmNCLButtonDown          = 0x10, 0xA1
	wmHotkey                          = 0x312
	wmExitSizeMove                    = 0x232
	htCaption                         = 2
	swHide, swShowNoActivate, swShow  = 0, 4, 5
)

type rect struct{ Left, Top, Right, Bottom int32 }

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

// 悬浮窗的偏好，存在数据目录 desktop.json
type deskSettings struct {
	X, Y             int32
	HasPos           bool
	Pinned           bool
	Hidden           bool
	Compact          bool
	Dock             string    // ""、"left"、"right"、"top"
	NoAutoHide       bool      // 停靠后不自动收起（默认收起）
	NoFullscreenHide bool      // 全屏时不让开（默认让开）
	DndUntil         int64     // 勿扰到这个时间（毫秒）；期间不弹系统通知和气泡
	Panel            panelRect // 完整面板窗口上次的位置和大小
	NoAutoUpdate     bool      // 关掉自动更新（默认每天检查一次，有新版就在空闲时更新）
	Sound            string    // 通知提示音：""（默认，不响）、内置 ID，或 "file:名字.wav"（数据目录 sounds 里）
	Theme            string    // 主题色 ID，取 themes 里的一个；"" = 默认翠绿
	Lang             string    // 界面语言："" 跟系统、"zh"、"en"（启动时在 main 里读，见 langPref）
	Strip            bool      // 贴边收起时缩成一根细条（每个任务一段颜色）；鼠标停上去只露出当前任务的进度卡，点了才展开
	ToastLen         string    // 右下角提醒卡停留多久："short"（一半）、""（标准）、"long"（两倍）
}

// toastLens 提醒卡停留时长的可选值（页面按倍数算，存进设置前按这个校验）
var toastLens = []string{"short", "", "long"}

func (f *floatWin) toastLen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set.ToastLen
}

func (f *floatWin) strip() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set.Strip
}

// pageURL 悬浮窗、提醒卡、托盘菜单、面板的页面地址，带上界面语言
func pageURL(port int, query string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/?%s&lang=%s", port, query, curLang())
}

// colorTheme 主题色：进度条、进度环、未读角标、开关、流光和“进行中”点阵都用它。
// 表示状态的颜色（做完绿、等你橙、出错红）不跟着变
type colorTheme struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Brand string `json:"brand"`
	Hi    string `json:"hi"`
	Lo    string `json:"lo"`
}

var themes = []colorTheme{
	{"green", "翠绿", "#4CB782", "#6EE0A6", "#2C9C66"},
	{"blue", "湖蓝", "#4EA7FC", "#8CC8FF", "#2F7FD6"},
	{"violet", "紫罗兰", "#8B7CF6", "#B4A9FF", "#6654D9"},
	{"pink", "樱粉", "#F472B6", "#FBA3D0", "#D9468F"},
	{"amber", "琥珀", "#F2B33D", "#FFD27A", "#C98A14"},
	{"cyan", "青碧", "#2DD4BF", "#7DEBDD", "#14A493"},
}

func (f *floatWin) theme() colorTheme {
	f.mu.Lock()
	id := f.set.Theme
	f.mu.Unlock()
	if i := slices.IndexFunc(themes, func(t colorTheme) bool { return t.ID == id }); i >= 0 {
		return themes[i]
	}
	return themes[0]
}

func (f *floatWin) dnd() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return now() < f.set.DndUntil
}

// setDnd 开关勿扰；开启时持续 1 小时，到点自动关掉并通知页面和托盘
func (f *floatWin) setDnd(on bool) {
	until := int64(0)
	if on {
		until = now() + int64(time.Hour/time.Millisecond)
	}
	f.mu.Lock()
	f.set.DndUntil = until
	f.mu.Unlock()
	f.saveSettings()
	f.pushHostState()
	f.changed()
	if on {
		time.AfterFunc(time.Hour+time.Second, func() {
			if !f.dnd() {
				f.pushHostState()
				f.changed()
			}
		})
	}
}

// pushHostState 把宿主侧状态告诉页面：置顶、勿扰、提示音，以及窗口在屏幕哪一半
// （抽屉往屏幕中间那一侧拉开：窗口变宽时靠右的保持右边缘不动，见 fit）
func (f *floatWin) pushHostState() {
	pinned, dnd, sound, strip, tl := f.pinned(), f.dnd(), f.soundState(), f.strip(), f.toastLen()
	th, _ := json.Marshal(f.theme())
	h, v := f.side()
	f.dispatch(func() {
		f.w.Eval(fmt.Sprintf("window.tickHostState && tickHostState({pinned:%t,dnd:%t,side:%q,vside:%q,glass:%t,strip:%t,toastLen:%q,theme:%s,%s})", pinned, dnd, h, v, glassOn, strip, tl, th, sound))
	})
}

// watchdog 每 20 秒看两件事：
//   - 界面线程还活着吗：往窗口线程派一个空任务，15 秒没执行就是卡死了。有守护进程时直接退出，让它重启；
//   - 页面还活着吗：窗口看得见时页面每 15 秒报一次心跳，90 秒没报（渲染进程崩了、页面白了）就重新加载页面。
//     窗口藏起来时浏览器会限流计时器，这段时间不算
func (f *floatWin) watchdog() {
	f.lastBeat.Store(now())
	url := pageURL(f.srv.port, "view=float")
	for range time.Tick(20 * time.Second) {
		alive := make(chan struct{}, 1)
		f.dispatch(func() { alive <- struct{}{} })
		select {
		case <-alive:
		case <-time.After(15 * time.Second):
			log.Printf("界面线程 15 秒没有响应")
			if isWorker() {
				os.Exit(exitHung) // 守护进程会重启
			}
			continue
		}
		if !f.visible() || f.fsHidden.Load() {
			f.lastBeat.Store(now())
			continue
		}
		if now()-f.lastBeat.Load() > 90_000 {
			log.Printf("悬浮窗页面 90 秒没有心跳，重新加载")
			f.lastBeat.Store(now())
			f.dispatch(func() { f.w.Navigate(url) })
		}
	}
}

// side 窗口中心在所在屏幕的哪一半（左 / 右、上 / 下），和 fit 的锚定规则一致
func (f *floatWin) side() (string, string) {
	r, wa := f.windowRect(), f.workArea()
	h, v := "left", "top"
	if (r.Left+r.Right)/2 > (wa.Left+wa.Right)/2 {
		h = "right"
	}
	if (r.Top+r.Bottom)/2 > (wa.Top+wa.Bottom)/2 {
		v = "bottom"
	}
	return h, v
}

type floatWin struct {
	srv  *Server
	w    webview2.WebView
	hwnd uintptr
	orig uintptr
	file string
	mu   sync.Mutex
	set  deskSettings
	cssW float64 // 当前内容宽高（CSS 像素）
	cssH float64
	// 环形列表展开期间钉住的屏幕点（鼠标所在处）：页面报尺寸时连同圆心位置一起报，窗口摆到让圆心落在这一点上。
	// 只在界面线程（页面回调）里读写
	anchor   point
	anchorOn bool
	shown    bool
	onChange func() // 置顶 / 显示状态变了，托盘菜单跟着改；托盘线程设置，UI 线程调用，用 mu 保护
	// 停靠：dock 只在 dockLoop 里读写，别的线程通过 dockReq 投递；tucked 页面和 fit 也要读，用原子量
	hotkey   string // 注册成功的全局快捷键，空表示没注册上
	dataPath string // WebView2 数据目录
	dock     dockRuntime
	dockReq  chan func()
	tucked   atomic.Bool
	fsHidden atomic.Bool  // 因全屏让开了
	lastBeat atomic.Int64 // 页面上次报心跳的时间（毫秒）
}

// inDock 把一个操作交给 dockLoop 执行；队列满了就丢，下一轮轮询会自己纠正
func (f *floatWin) inDock(fn func()) {
	select {
	case f.dockReq <- fn:
	default:
	}
}

func (f *floatWin) setOnChange(fn func()) {
	f.mu.Lock()
	f.onChange = fn
	f.mu.Unlock()
}

func (f *floatWin) changed() {
	f.mu.Lock()
	fn := f.onChange
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
}

var theFloat *floatWin

func runDesktop(srv *Server, serve func()) {
	pSetDpiAwareness.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	initGlass()                        // 要在建第一个 WebView 之前
	dir := filepath.Dir(srv.store.file)
	f := &floatWin{srv: srv, file: filepath.Join(dir, "desktop.json"), set: deskSettings{Pinned: true}, cssW: 340, cssH: 160, dockReq: make(chan func(), 16),
		dataPath: filepath.Join(dir, "webview")} // 两个窗口共用一份 WebView2 数据，共享浏览器进程，省内存
	if b, err := os.ReadFile(f.file); err == nil {
		_ = json.Unmarshal(b, &f.set)
	}
	theFloat = f
	srv.showFloat = func() { f.dispatch(f.summon) }
	srv.showPanel = func(hash string) { openPanel(srv, f, hash) }
	srv.theme = func() any { return f.theme() }

	f.w = newGlassWebView(webview2.WebViewOptions{
		DataPath:      f.dataPath,
		WindowOptions: webview2.WindowOptions{Title: "Tick", Width: 340, Height: 160},
	})
	if f.w == nil {
		fatal(L("没能创建悬浮窗：需要 Microsoft Edge WebView2 运行时。\n服务仍可以用 tick -headless 启动，在浏览器里打开。",
			"Could not create the window: Tick needs the Microsoft Edge WebView2 runtime.\nYou can still run tick -headless and open it in a browser."))
	}
	f.hwnd = uintptr(f.w.Window())
	f.style()
	f.bind()
	// 全局快捷键 Ctrl+Alt+T（必须在拥有窗口的线程上注册）；被别的程序占了就不用，托盘里会写明
	if ok, _, err := pRegisterHotKey.Call(f.hwnd, 1, 0x1|0x2|0x4000, 'T'); ok != 0 { // MOD_ALT|MOD_CONTROL|MOD_NOREPEAT
		f.hotkey = "Ctrl+Alt+T"
	} else {
		log.Printf("全局快捷键 Ctrl+Alt+T 注册失败（可能被占用）：%v", err)
	}
	// Ctrl+Alt+→ / ← 在进行中的任务之间切换（收着时露出那个任务的进度卡）
	for id, vk := range map[uintptr]uintptr{2: 0x27, 3: 0x25} {
		if ok, _, err := pRegisterHotKey.Call(f.hwnd, id, 0x1|0x2|0x4000, vk); ok == 0 {
			log.Printf("切换任务的快捷键注册失败（可能被占用）：%v", err)
		}
	}
	setupNotices(srv, f)
	serve()
	go runTray(srv, f)
	go f.dockLoop()
	go f.watchdog()
	f.w.Navigate(pageURL(srv.port, "view=float"))
	// 页面 1.5 秒内没报就绪也先显示出来，免得看不到窗口
	time.AfterFunc(1500*time.Millisecond, func() { f.dispatch(func() { f.ready() }) })
	f.w.Run()
	f.saveSettings()
}

func (f *floatWin) dispatch(fn func()) { f.w.Dispatch(fn) }

// style 把 go-webview2 默认的普通窗口改成：无边框、不进任务栏、圆角、深色、按偏好置顶
func (f *floatWin) style() {
	h := f.hwnd
	pShowWindow.Call(h, swHide)
	pSetWindowLongPtr.Call(h, uintptr(gwlStyleU()), wsPopup|wsClipChildren)
	ex := uintptr(wsExToolWindow)
	pSetWindowLongPtr.Call(h, uintptr(gwlExStyleU()), ex)
	dark, round := int32(1), int32(2)
	pDwmSetWindowAttribute.Call(h, 20, uintptr(unsafe.Pointer(&dark)), 4)  // 深色非客户区
	pDwmSetWindowAttribute.Call(h, 33, uintptr(unsafe.Pointer(&round)), 4) // Win11 圆角
	border := uint32(0x00332F2E)                                           // 1px 边框，COLORREF 是 BGR
	pDwmSetWindowAttribute.Call(h, 34, uintptr(unsafe.Pointer(&border)), 4)
	applyGlass(h, true)
	f.orig, _, _ = pGetWindowLongPtr.Call(h, uintptr(gwlpWndProcU()))
	pSetWindowLongPtr.Call(h, uintptr(gwlpWndProcU()), syscall.NewCallback(floatProc))
	x, y := f.defaultPos()
	if f.set.HasPos {
		x, y = f.set.X, f.set.Y
	}
	w, hh := f.physical(f.cssW, f.cssH)
	pSetWindowPos.Call(h, f.zorder(), uintptr(x), uintptr(y), uintptr(w), uintptr(hh), swpNoActivate|swpFrameChanged)
}

// go 的常量是无类型的负数，转成 uintptr 需要先过 int
func gwlStyleU() int   { return gwlStyle }
func gwlExStyleU() int { return gwlExStyle }
func gwlpWndProcU() int {
	return gwlpWndProc
}

func floatProc(hwnd, msg, wp, lp uintptr) uintptr {
	f := theFloat
	switch msg {
	case wmClose: // Alt+F4 只是收起，退出走托盘
		f.hide()
		return 0
	case wmHotkey: // 全局快捷键：1 = 叫出或收起；2 / 3 = 切到下一个 / 上一个任务（页面决定怎么显示）
		switch wp {
		case 2, 3:
			dir := 1
			if wp == 3 {
				dir = -1
			}
			f.w.Eval(fmt.Sprintf("window.tickCycle && tickCycle(%d)", dir))
		default:
			f.toggle()
		}
		return 0
	case wmExitSizeMove:
		f.rememberPos()
	}
	r, _, _ := pCallWindowProc.Call(f.orig, hwnd, msg, wp, lp)
	return r
}

func (f *floatWin) zorder() uintptr {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.set.Pinned {
		return ^uintptr(0) // HWND_TOPMOST = -1
	}
	return ^uintptr(1) // HWND_NOTOPMOST = -2
}

func (f *floatWin) scale() float64 {
	dpi, _, _ := pGetDpiForWindow.Call(f.hwnd)
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

func (f *floatWin) physical(w, h float64) (int32, int32) {
	s := f.scale()
	return int32(math.Round(w * s)), int32(math.Round(h * s))
}

func (f *floatWin) workArea() rect {
	mon, _, _ := pMonitorFromWindow.Call(f.hwnd, 2) // MONITOR_DEFAULTTONEAREST
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	return mi.Work
}

// 默认放在工作区右侧、偏上：右下角是 Windows 通知弹出的位置，会被盖住；
// 放在上半部分，窗口随内容向下长，也避开浏览器右上角的关闭按钮
func (f *floatWin) defaultPos() (int32, int32) {
	wa := f.workArea()
	w, _ := f.physical(f.cssW, f.cssH)
	m := int32(math.Round(16 * f.scale()))
	return wa.Right - w - m, wa.Top + (wa.Bottom-wa.Top)*16/100
}

func (f *floatWin) windowRect() rect {
	var r rect
	pGetWindowRect.Call(f.hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func (f *floatWin) rememberPos() {
	r := f.windowRect()
	f.mu.Lock()
	f.set.X, f.set.Y, f.set.HasPos = r.Left, r.Top, true
	f.mu.Unlock()
	f.saveSettings()
}

func (f *floatWin) saveSettings() {
	f.mu.Lock()
	b, _ := json.Marshal(f.set)
	f.mu.Unlock()
	if err := os.WriteFile(f.file, b, 0o644); err != nil {
		log.Printf("悬浮窗设置没存上：%v", err)
	}
}

// fit 按内容调窗口大小。窗口在屏幕下半部分时保持底边不动往上长，否则保持顶边
// setAnchor 把页面坐标 (x, y) 对应的屏幕点钉住，返回这一点到工作区四边的距离（CSS 像素），
// 页面据此决定任务往哪边散开。x < 0 表示取消
func (f *floatWin) setAnchor(x, y float64) map[string]float64 {
	if x < 0 {
		f.anchorOn = false
		return nil
	}
	r, wa, s := f.windowRect(), f.workArea(), f.scale()
	f.anchor = point{r.Left + int32(math.Round(x*s)), r.Top + int32(math.Round(y*s))}
	f.anchorOn = true
	d := func(v int32) float64 { return math.Round(float64(v) / s) }
	return map[string]float64{"l": d(f.anchor.X - wa.Left), "r": d(wa.Right - f.anchor.X), "u": d(f.anchor.Y - wa.Top), "d": d(wa.Bottom - f.anchor.Y)}
}

// fitAt 和 fit 一样按内容调尺寸，但窗口的位置让页面里的 (ax, ay) 落在钉住的屏幕点上（放不下时贴着工作区边）
func (f *floatWin) fitAt(cssW, cssH, ax, ay float64) {
	if !f.anchorOn {
		f.fit(cssW, cssH)
		return
	}
	f.cssW, f.cssH = math.Max(24, math.Min(cssW, 900)), math.Max(24, math.Min(cssH, 900))
	wa, s := f.workArea(), f.scale()
	w, h := f.physical(f.cssW, f.cssH)
	x := f.anchor.X - int32(math.Round(ax*s))
	y := f.anchor.Y - int32(math.Round(ay*s))
	x = max(wa.Left, min(x, wa.Right-w))
	y = max(wa.Top, min(y, wa.Bottom-h))
	pSetWindowPos.Call(f.hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpNoZOrder|swpNoActivate)
}

func (f *floatWin) fit(cssW, cssH float64) {
	// 下限放到 6：贴边细条只有 8px 粗
	// 上限 720 宽：通知抽屉拉开时是主面板加抽屉的宽度
	f.cssW, f.cssH = math.Max(6, math.Min(cssW, 720)), math.Max(6, math.Min(cssH, 720))
	old := f.windowRect()
	wa := f.workArea()
	w, h := f.physical(f.cssW, f.cssH)
	x, y := old.Left, old.Top
	if (old.Top+old.Bottom)/2 > (wa.Top+wa.Bottom)/2 {
		y = old.Bottom - h
	}
	if right := old.Right; (old.Left+old.Right)/2 > (wa.Left+wa.Right)/2 {
		x = right - w // 靠右放时保持右边缘不动
	}
	x = max(wa.Left, min(x, wa.Right-w))
	y = max(wa.Top, min(y, wa.Bottom-h))
	// 停靠时尺寸变了也要继续贴着边（收起状态下保持只露窄边）
	if edge := f.dockEdge(); edge != "" {
		// 贴边时：顶边固定（左右停靠）或左边固定（上停靠），从标签原地长出面板，再贴齐那条边
		x, y = f.dockPos(edge, rect{old.Left, old.Top, old.Left + w, old.Top + h})
	}
	pSetWindowPos.Call(f.hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpNoZOrder|swpNoActivate)
}

type point struct{ X, Y int32 }

var (
	dragging sync.Mutex
	dragEnd  atomic.Bool
)

// followCursor 按真实光标位置挪窗口，直到页面报告松开（tickDragEnd）。
// 不用 GetAsyncKeyState 判断松开：前台窗口属于别的进程时它会一直返回 0；
// 也不用页面坐标：WebView2 在指针捕获期间给的 screenX/Y 不更新
func (f *floatWin) followCursor() {
	if !dragging.TryLock() {
		return
	}
	defer dragging.Unlock()
	isDragging.Store(true)
	defer isDragging.Store(false)
	var start, cur point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&start)))
	r := f.windowRect()
	last := start
	deadline := time.Now().Add(2 * time.Minute) // 万一没收到松开，别一直跟着光标
	for !dragEnd.Load() && time.Now().Before(deadline) {
		pGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
		if cur != last {
			last = cur
			x, y := r.Left+cur.X-start.X, r.Top+cur.Y-start.Y
			pSetWindowPos.Call(f.hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
		}
		time.Sleep(8 * time.Millisecond)
	}
	if last != start {
		f.inDock(f.afterDrag) // 吸附判断和停靠状态都归 dockLoop 管
	}
}

func (f *floatWin) ready() {
	f.pushHostState() // 页面每次加载完都要拿到置顶、勿扰、提示音和所在半边
	if f.shown {
		return
	}
	f.shown = true
	f.mu.Lock()
	hidden := f.set.Hidden
	f.mu.Unlock()
	if !hidden {
		f.show(false)
	}
	f.notifyDock()
}

func (f *floatWin) visible() bool {
	v, _, _ := pIsWindowVisible.Call(f.hwnd)
	return v != 0
}

func (f *floatWin) show(activate bool) {
	cmd := uintptr(swShowNoActivate)
	if activate {
		cmd = swShow
	}
	pShowWindow.Call(f.hwnd, cmd)
	pSetWindowPos.Call(f.hwnd, f.zorder(), 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	if activate {
		pSetForegroundWindow.Call(f.hwnd)
	}
	f.setHidden(false)
	f.w.Eval("window.tickVisible && tickVisible(true)")
}

func (f *floatWin) hide() {
	pShowWindow.Call(f.hwnd, swHide)
	f.setHidden(true)
	f.w.Eval("window.tickVisible && tickVisible(false)") // 看不见就停掉动画和每秒计时，省 CPU
}

func (f *floatWin) setHidden(v bool) {
	f.mu.Lock()
	changed := f.set.Hidden != v
	f.set.Hidden = v
	f.mu.Unlock()
	if changed {
		f.saveSettings()
		f.changed()
	}
}

// toggle 托盘左键：显示时先让页面播收起动画再隐藏；隐藏或收在边上时叫出来
func (f *floatWin) toggle() {
	f.dispatch(func() {
		if f.visible() && !f.tucked.Load() {
			f.w.Eval("window.tickLeave ? tickLeave() : tickHide()")
		} else {
			f.summon()
		}
	})
}

// summon 从托盘叫回：显示、滑出边缘、播一次出现动画，停留几秒再按自动隐藏规则处理
func (f *floatWin) summon() {
	f.show(true)
	f.w.Eval("window.tickAppear && tickAppear()")
	f.inDock(func() { f.peek(false, 3*time.Second) })
}

func (f *floatWin) setPinned(on bool) {
	f.mu.Lock()
	f.set.Pinned = on
	f.mu.Unlock()
	f.saveSettings()
	pSetWindowPos.Call(f.hwnd, f.zorder(), 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	f.w.Eval(fmt.Sprintf("window.tickHostState && tickHostState({pinned:%t})", on))
	f.changed()
}

func (f *floatWin) pinned() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set.Pinned
}

// 页面里能调的宿主函数（go-webview2 在 UI 线程上回调）
func (f *floatWin) bind() {
	must := func(err error) {
		if err != nil {
			log.Printf("绑定宿主函数失败：%v", err)
		}
	}
	// 拖动在宿主侧完成：WebView2 的输入窗口属于浏览器进程，HTCAPTION 拖动和页面里的指针坐标都靠不住。
	// 页面在标题栏按下时通知这里，按住左键期间按真实光标位置挪窗口，松开后记住位置
	must(f.w.Bind("tickDrag", func() {
		dragEnd.Store(false) // 在启动跟随前清掉，免得紧接着到的松开被覆盖
		go f.followCursor()
	}))
	must(f.w.Bind("tickDragEnd", func() { dragEnd.Store(true) }))
	// 点标签：马上展开，并且停留一会儿再按自动隐藏规则收回
	must(f.w.Bind("tickUntuck", func() { f.inDock(func() { f.peek(false, 1500*time.Millisecond) }) }))
	must(f.w.Bind("tickFit", func(w, h float64) { f.fit(w, h) }))
	must(f.w.Bind("tickFitAt", func(w, h, ax, ay float64) { f.fitAt(w, h, ax, ay) }))
	must(f.w.Bind("tickAnchor", func(x, y float64) map[string]float64 { return f.setAnchor(x, y) }))
	must(f.w.Bind("tickReady", func() { f.ready() }))
	must(f.w.Bind("tickHide", func() { f.hide() }))
	must(f.w.Bind("tickDnd", func(on bool) bool { f.setDnd(on); return on }))
	must(f.w.Bind("tickPin", func(on bool) bool { f.setPinned(on); return on }))
	must(f.w.Bind("tickState", func() map[string]any {
		f.mu.Lock()
		defer f.mu.Unlock()
		return map[string]any{"pinned": f.set.Pinned, "compact": f.set.Compact, "dnd": now() < f.set.DndUntil, "hotkey": f.hotkey}
	}))
	must(f.w.Bind("tickCompact", func(on bool) {
		f.mu.Lock()
		f.set.Compact = on
		f.mu.Unlock()
		f.saveSettings()
	}))
	must(f.w.Bind("tickOpen", func(url string) error { return openExternal(url) }))
	must(f.w.Bind("tickPanel", func(hash string) { openPanel(f.srv, f, hash) })) // hash 如 "#s_ab12cd"、"#guide"
	must(f.w.Bind("tickSound", func(id string) { go f.setSound(id) }))           // 换提示音并试听；"" = 不响
	must(f.w.Bind("tickSoundDir", f.openSoundDir))
	must(f.w.Bind("tickBeat", func() { f.lastBeat.Store(now()) }))                           // 页面心跳，见 watchdog
	must(f.w.Bind("tickToast", f.toast))                                                     // 右下角提醒卡（JSON）
	must(f.w.Bind("tickTheme", func(dark bool) { setDarkFrame(f.hwnd, dark) }))              // 毛玻璃的着色跟页面主题走
	must(f.w.Bind("tickClear", func(on bool) bool { setClear(f.hwnd, on); return glassOn })) // 环形列表：窗口透明
}

// openExternal 用系统默认程序打开链接（浏览器、codex:// 等）；拒绝本地文件和脚本协议
func openExternal(url string) error {
	if !linkAllowed(url) { // 和存储时同一份白名单，防止页面传来别的协议
		return fmt.Errorf("不支持的链接")
	}
	verb, _ := windows.UTF16PtrFromString("open")
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

// ---------- 托盘 ----------

func runTray(srv *Server, f *floatWin) {
	runtime.LockOSThread()
	systray.Run(func() {
		systray.SetIcon(iconICO(iconState{Progress: 1}))
		systray.SetTooltip("Tick")
		t := &tray{f: f, srv: srv}
		// 系统菜单只在宫格弹窗建不出来时才会出现（后备）；动作和弹窗共用 t.act
		mShow := systray.AddMenuItem(L("收到托盘", "Hide to tray"), "")
		mPin := systray.AddMenuItemCheckbox(L("悬浮窗置顶", "Keep on top"), "", f.pinned())
		mAutoHide := systray.AddMenuItemCheckbox(L("贴边后自动隐藏", "Auto-hide when docked"), L("拖到屏幕左、右、上边缘松手即可贴边", "Drag to the left, right or top edge to dock"), f.autoHide())
		mFS := systray.AddMenuItemCheckbox(L("全屏时让开", "Hide in fullscreen"), L("游戏、视频、演示全屏时自动隐藏", "Hide while games, videos or slides are fullscreen"), f.fullscreenHide())
		mDnd := systray.AddMenuItemCheckbox(L("勿扰 1 小时", "Do not disturb, 1 hour"), L("不弹提醒卡和气泡；悬浮窗里照常记录", "No alerts or bubbles; the window still records everything"), f.dnd())
		mSound := systray.AddMenuItem(L("通知声音", "Notification sound"), "")
		soundOpts := append([]soundOpt{{"", L("无", "None")}}, f.sounds()...)
		soundItems := make([]*systray.MenuItem, len(soundOpts))
		for i, o := range soundOpts {
			soundItems[i] = mSound.AddSubMenuItemCheckbox(o.Name, "", o.ID == f.soundID())
			go func(i int, id string) {
				for range soundItems[i].ClickedCh {
					t.act("sound", true, id)
				}
			}(i, o.ID)
		}
		mSoundDir := mSound.AddSubMenuItem(L("打开声音文件夹…", "Open sound folder…"), L("把自己的 .wav 放进去，重开 Tick 后出现在这里", "Put your own .wav files here; they show up after restarting Tick"))
		systray.AddSeparator()
		mPanel := systray.AddMenuItem(L("打开面板", "Open panel"), L("完整面板：历史、搜索、编辑", "Full panel: history, search, editing"))
		mBrowser := systray.AddMenuItem(L("在浏览器里打开", "Open in browser"), "")
		mMCP := systray.AddMenuItem(L("复制 MCP 地址", "Copy MCP address"), "")
		mAuto := systray.AddMenuItemCheckbox(L("开机启动", "Start with Windows"), "", autostartEnabled())
		mUpdate := systray.AddMenuItem(L("检查更新（当前 ", "Check for updates (current ")+version+L("）", ")"), "")
		mAutoUp := systray.AddMenuItemCheckbox(L("自动更新", "Auto-update"), L("每 12 小时检查一次，空闲时重启到新版本", "Checks every 12 hours and restarts into the new version when idle"), !f.set.NoAutoUpdate)
		t.up = &updater{f: f, srv: srv, menu: mUpdate}
		srv.restart = func(force bool) { t.up.setPending(L("新版本", "new version")); t.up.restartWhenIdle(force) }
		go t.up.loop()
		systray.AddSeparator()
		mQuit := systray.AddMenuItem(L("退出 Tick", "Quit Tick"), "")
		key := ""
		if f.hotkey != "" {
			key = "\t" + f.hotkey // 菜单里右对齐显示快捷键
		}
		check := func(m *systray.MenuItem, on bool) {
			if on {
				m.Check()
			} else {
				m.Uncheck()
			}
		}
		t.refresh = func() {
			st := t.state()
			if st.Visible {
				mShow.SetTitle(L("收到托盘", "Hide to tray") + key)
			} else {
				mShow.SetTitle(L("显示悬浮窗", "Show window") + key)
			}
			check(mDnd, st.Dnd)
			check(mPin, st.Pinned)
			check(mAutoHide, st.AutoHide)
			check(mFS, st.FsHide)
			check(mAuto, st.Autostart)
			check(mAutoUp, st.AutoUpdate)
			for i, o := range soundOpts {
				check(soundItems[i], o.ID == st.Sound)
			}
		}
		f.setOnChange(t.refresh)
		t.refresh()
		theTray = t
		systray.SetOnTapped(func() { // 左键开关悬浮窗；点了就是看过了：通知全部算已读，托盘停闪，未读预览收起
			thePeek.hide()
			if srv.store.UnreadNotices() > 0 {
				_, _ = srv.store.ReadNotices(ReadInput{All: true})
			}
			f.toggle()
		})
		systray.SetOnSecondaryTapped(func() { // 右键：宫格弹窗；建不出来时 open 返回 false，之后改出系统菜单
			if !theMenu.open(t) {
				systray.SetOnSecondaryTapped(nil)
			}
		})
		go func() {
			for {
				select {
				case <-mShow.ClickedCh:
					t.act("float", true, "")
				case <-mPin.ClickedCh:
					t.act("pin", !mPin.Checked(), "")
				case <-mAutoHide.ClickedCh:
					t.act("autohide", !mAutoHide.Checked(), "")
				case <-mDnd.ClickedCh:
					t.act("dnd", !f.dnd(), "")
				case <-mFS.ClickedCh:
					t.act("fs", !mFS.Checked(), "")
				case <-mSoundDir.ClickedCh:
					t.act("sounddir", true, "")
				case <-mPanel.ClickedCh:
					t.act("panel", true, "")
				case <-mBrowser.ClickedCh:
					t.act("browser", true, "")
				case <-mMCP.ClickedCh:
					t.act("mcp", true, "")
				case <-mUpdate.ClickedCh:
					t.act("update", true, "")
				case <-mAutoUp.ClickedCh:
					t.act("autoupdate", !mAutoUp.Checked(), "")
				case <-mAuto.ClickedCh:
					t.act("autostart", !mAuto.Checked(), "")
				case <-mQuit.ClickedCh:
					t.act("quit", true, "")
					return
				}
			}
		}()
		go trayStatus(srv, f)
		go trayHover(srv, f)
	}, nil)
}

var theTray *tray

// trayFlash 让托盘图标闪 n 下（亮帧和平常帧交替）；n = 0 表示马上停
var trayFlash = make(chan int, 4)

// trayStatus 跟着数据变化更新托盘提示和图标，并负责闪动。
// 图标上的环是活的：弧长 = 进行中会话的总完成比例（按 1/24 取整，免得每勾一项都重画），有人在等你时变橙
func trayStatus(srv *Server, f *floatWin) {
	ch, _ := srv.store.Subscribe()
	// 有没读的通知就一直闪（像微信），读完 / 清空 / 开了勿扰就停；勿扰到点了也要重新判断
	dndCheck := time.NewTicker(30 * time.Second)
	defer dndCheck.Stop()
	nag := false
	cache := map[iconState][]byte{}
	base := iconState{Progress: 1}
	shown := iconState{Progress: -1}
	set := func(k iconState) {
		if k == shown {
			return
		}
		b, ok := cache[k]
		if !ok {
			b = iconICO(k)
			cache[k] = b
		}
		systray.SetIcon(b)
		shown = k
	}
	flash := iconState{Progress: 1, Tone: toneFlash}
	lastTip := ""
	var blink *time.Ticker
	var tickC <-chan time.Time
	left := 0
	stop := func() {
		if blink != nil {
			blink.Stop()
			blink, tickC = nil, nil
		}
		left = 0
		set(base)
	}
	update := func() {
		running, waiting, done, total := 0, 0, 0, 0
		for _, s := range srv.store.Sessions(false) {
			active := isWaiting(s.Status) || s.Status == "running" || s.Status == "planning"
			switch {
			case isWaiting(s.Status):
				waiting++
			case active:
				running++
			}
			if active {
				for _, it := range s.Items {
					total++
					if it.Status == "done" || it.Status == "skipped" {
						done++
					}
				}
			}
		}
		tip := L("Tick · 没有进行中的会话", "Tick · nothing running")
		if running+waiting > 0 {
			tip = fmt.Sprintf(L("Tick · %d 进行中", "Tick · %d running"), running)
			if waiting > 0 {
				tip += fmt.Sprintf(L(" · %d 等你", " · %d waiting"), waiting)
			}
			if total > 0 {
				tip += fmt.Sprintf(L(" · 清单 %d/%d", " · %d/%d done"), done, total)
			}
		}
		if tip != lastTip {
			systray.SetTooltip(tip)
			lastTip = tip
		}
		base = iconState{Progress: 1}
		if running+waiting > 0 {
			base.Progress = 0
			if total > 0 {
				base.Progress = math.Round(float64(done)/float64(total)*24) / 24
			}
		}
		if waiting > 0 {
			base.Tone = toneOrange
		}
		nag = srv.store.UnreadNotices() > 0 && !f.dnd()
		switch {
		case nag && blink == nil:
			blink = time.NewTicker(520 * time.Millisecond)
			tickC = blink.C
			left = 1
			set(flash)
		case !nag && blink != nil && left < 0:
			stop() // 读完了：停在平常的图标上
		case blink == nil:
			set(base)
		}
		if nag {
			left = -1 // 一直闪，直到 nag 变成 false
		}
	}
	update()
	// 一阵推送合成一次刷新：多个 agent 同时勾选时不必每条都遍历所有会话、调系统托盘接口
	var coalesce <-chan time.Time
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
			if coalesce == nil {
				coalesce = time.After(200 * time.Millisecond)
			}
		case <-coalesce:
			coalesce = nil
			update()
		case <-dndCheck.C:
			update()
		case n := <-trayFlash:
			if n <= 0 {
				if !nag {
					stop()
				}
				continue
			}
			if left < 0 {
				continue // 已经在一直闪
			}
			left = max(left, n*2) // 正在闪就接着闪，不叠加
			if blink == nil {
				blink = time.NewTicker(420 * time.Millisecond)
				tickC = blink.C
				set(flash)
				left--
			}
		case <-tickC:
			if left < 0 { // 有未读：亮帧和平常帧来回换
				if shown == flash {
					set(base)
				} else {
					set(flash)
				}
				continue
			}
			if left == 0 {
				stop()
				continue
			}
			left--
			if left%2 == 1 {
				set(flash)
			} else {
				set(base)
			}
		}
	}
}

func copyText(s string) {
	cmd := exec.Command("clip")
	cmd.Stdin = strings.NewReader(s)
	hideConsole(cmd)
	if err := cmd.Run(); err != nil {
		log.Printf("复制到剪贴板失败：%v", err)
	}
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("Tick")
	return err == nil
}

func setAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		return k.DeleteValue("Tick")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue("Tick", `"`+exe+`"`)
}

// ---------- 通知 ----------
// 不用 Windows 系统通知：提醒卡（右下角）、托盘闪动和提示音见 toast_windows.go。
// 这里只管“等你 / 受阻”时把收到托盘的悬浮窗叫出来（不抢焦点）；收在边上时由页面把标签变成气泡。
// 勿扰期间不叫出来，悬浮窗里照常记下
func setupNotices(srv *Server, f *floatWin) {
	srv.store.SetOnNotice(func(n *Notice) {
		if f.dnd() || !isWaiting(n.Kind) {
			return
		}
		f.dispatch(func() {
			if !f.visible() && !f.fsHidden.Load() {
				f.show(false)
			}
		})
	})
}

// ---------- 杂项 ----------

// showExisting：第二次启动时，叫已在运行的那个把悬浮窗显示出来
func showExisting(port int) error {
	c := http.Client{Timeout: 2 * time.Second}
	r, err := c.Post(fmt.Sprintf("http://127.0.0.1:%d/api/desktop/show", port), "application/json", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	return r.Body.Close()
}

// attachConsole：tick.exe 是窗口程序，从终端直接运行命令行时没有标准输出。
// 输出被管道接走（agent 调用的情况）就不用管；否则接到父进程的控制台上
func attachConsole() {
	if h, _ := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); h != 0 && h != windows.InvalidHandle {
		return
	}
	if r, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole").Call(^uintptr(0)); r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
}

func execCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	hideConsole(cmd)
	return cmd
}

func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

func fatal(msg string) {
	log.Print(msg)
	t, _ := windows.UTF16PtrFromString(msg)
	c, _ := windows.UTF16PtrFromString("Tick")
	pMessageBox.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x10)
	if isWorker() {
		os.Exit(exitFatal) // 已经告诉用户了，守护进程别再重启弹同一个窗
	}
	os.Exit(1)
}
