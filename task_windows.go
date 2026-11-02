//go:build windows

package main

// 任务子窗口：悬浮窗里双击某个任务（或点行上的「分离」）后，这个任务弹成一个独立的小窗口（view=task），
// 外观和悬浮窗一样（毛玻璃、圆角、同一套样式），里面是这个任务（展开着）。主窗口列表里暂时不再显示它。
// 关掉子窗口（叉号 / Esc）= 回到主窗口：任务重新出现在主列表里。
// 子窗口之间、子窗口和悬浮窗各自独立，一个任务最多一个子窗口。

import (
	"encoding/json"
	"log"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

type taskWin struct {
	f       *floatWin
	sid     string
	anchor  point // 分离时悬浮窗的位置（只用来决定子窗口摆在哪一侧）和被分离那一行的纵向位置
	fr      rect
	mu      sync.Mutex
	w       webview2.WebView
	hwnd    uintptr
	placed  bool    // 第一次排好版后摆好位置；之后只改大小，位置由用户拖
	saved   *pinRec // 重启恢复时：上次摆的位置（有的话第一次就摆在那里）
	dragEnd atomic.Bool
}

// monitorWork 包含点 pt（或离它最近）的那块显示器的工作区
func monitorWork(pt point) rect {
	mon, _, _ := pMonitorFromPoint.Call(uintptr(uint32(pt.X))|uintptr(uint32(pt.Y))<<32, 2)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	return mi.Work
}

// restoreTasks 启动后把上次钉住的任务浮窗按记住的位置恢复；会话已经没了的记录删掉
func restoreTasks(f *floatWin) {
	f.mu.Lock()
	pins := slices.Clone(f.set.Pins)
	f.mu.Unlock()
	for _, p := range pins {
		if s, err := f.srv.store.Session(p.SID); err != nil || s.Archived {
			f.pinDel(p.SID)
			continue
		}
		detachTask(f, p.SID, 0)
	}
}

var theTasks = struct {
	mu sync.Mutex
	m  map[string]*taskWin
}{m: map[string]*taskWin{}}

// isDetached 这个任务现在是不是钉成了独立浮窗
func isDetached(sid string) bool {
	theTasks.mu.Lock()
	defer theTasks.mu.Unlock()
	return theTasks.m[sid] != nil
}

// detachedIDs 现在分离着的任务（含正在创建窗口的），按 ID 排序
func detachedIDs() []string {
	theTasks.mu.Lock()
	defer theTasks.mu.Unlock()
	ids := make([]string, 0, len(theTasks.m))
	for id := range theTasks.m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// pushDetached 把分离名单推给悬浮窗页面，主窗口列表据此隐藏 / 恢复
func (f *floatWin) pushDetached() {
	b, _ := json.Marshal(detachedIDs())
	f.dispatch(func() { f.w.Eval("window.tickDetached && tickDetached(" + string(b) + ")") })
}

// detachTask 页面（悬浮窗）调：把 sid 这个任务分离成子窗口，cy 是它那一行在悬浮窗里的纵向中心（CSS 像素）
func detachTask(f *floatWin, sid string, cy float64) {
	if !strings.HasPrefix(sid, "s_") {
		return
	}
	if _, err := f.srv.store.Session(sid); err != nil {
		return
	}
	theTasks.mu.Lock()
	if t := theTasks.m[sid]; t != nil { // 已经分离着：叫到前面
		theTasks.mu.Unlock()
		t.mu.Lock()
		w, h := t.w, t.hwnd
		t.mu.Unlock()
		if w != nil {
			w.Dispatch(func() { pSetForegroundWindow.Call(h) })
		}
		return
	}
	r, sc := f.windowRect(), f.scale()
	t := &taskWin{f: f, sid: sid, fr: r, anchor: point{(r.Left + r.Right) / 2, r.Top + int32(cy*sc+0.5)}}
	if rec, ok := f.pinGet(sid); ok { // 重启恢复：位置用上次记的
		t.saved = &rec
	} else {
		f.pinSet(pinRec{SID: sid})
	}
	theTasks.m[sid] = t
	theTasks.mu.Unlock()
	log.Printf("[子窗口] 钉住 %s", sid)
	f.pushDetached()
	go t.run()
}

// finish 子窗口没了（用户关掉、创建失败）：任务回到主窗口
func (t *taskWin) finish() {
	theTasks.mu.Lock()
	delete(theTasks.m, t.sid)
	theTasks.mu.Unlock()
	t.f.pinDel(t.sid)
	log.Printf("[子窗口] 关闭 %s，取消钉住，任务回到主窗口", t.sid)
	t.f.pushDetached()
}

// run 在自己的线程上建窗口并跑消息循环，直到窗口被关掉
func (t *taskWin) run() {
	runtime.LockOSThread()
	f := t.f
	w := newGlassWebView(webview2.WebViewOptions{
		DataPath:      f.dataPath, // 和悬浮窗共用一份浏览器进程
		AutoFocus:     true,
		WindowOptions: webview2.WindowOptions{Title: "Tick · 任务", Width: 340, Height: 200},
	})
	if w == nil {
		log.Printf("任务子窗口创建失败")
		t.finish()
		return
	}
	h := uintptr(w.Window())
	pShowWindow.Call(h, swHide)
	pSetWindowLongPtr.Call(h, uintptr(gwlStyleU()), wsPopup|wsClipChildren)
	pSetWindowLongPtr.Call(h, uintptr(gwlExStyleU()), wsExToolWindow|wsExTopmost)
	round, border := int32(2), uint32(0x00332F2E)
	pDwmSetWindowAttribute.Call(h, 33, uintptr(unsafe.Pointer(&round)), 4)
	pDwmSetWindowAttribute.Call(h, 34, uintptr(unsafe.Pointer(&border)), 4)
	setDarkFrame(h, true)
	applyGlass(h, true)
	_ = w.Bind("tickTaskGlass", func() bool { return glassOn })
	_ = w.Bind("tickTaskFit", func(cw, ch float64) { t.fit(cw, ch) })
	_ = w.Bind("tickTaskClose", func() { pPostMessage.Call(h, wmClose, 0, 0) })
	_ = w.Bind("tickTaskDrag", func() {
		t.dragEnd.Store(false)
		go t.drag()
	})
	_ = w.Bind("tickTaskDragEnd", func() { t.dragEnd.Store(true) })
	_ = w.Bind("tickOpen", func(url string) error { return openExternal(url) })
	_ = w.Bind("tickPanel", func(hash string) { openPanel(f.srv, f, hash) })
	t.mu.Lock()
	t.w, t.hwnd = w, h
	t.mu.Unlock()
	w.Navigate(pageURL(f.srv.port, "view=task&sid="+t.sid))
	w.Run()
	t.mu.Lock()
	t.w, t.hwnd = nil, 0
	t.mu.Unlock()
	t.finish()
	w.Destroy()
}

// fit 页面排好版后报内容尺寸（CSS 像素）：窗口跟着内容长高；第一次摆在悬浮窗的一侧，对着被分离的那一行
func (t *taskWin) fit(cw, ch float64) {
	t.mu.Lock()
	h, placed := t.hwnd, t.placed
	t.mu.Unlock()
	if h == 0 {
		return
	}
	f := t.f
	sc := f.scale()
	wa := f.workArea()
	pw, ph := int32(cw*sc+0.5), int32(ch*sc+0.5)
	ph = min(ph, (wa.Bottom-wa.Top)*85/100)
	m := int32(8*sc + 0.5)
	var cur rect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&cur)))
	x, y := cur.Left, cur.Top
	switch {
	case !placed && t.saved != nil && t.saved.Has:
		x, y = t.saved.X, t.saved.Y // 重启恢复：回到上次的位置
	case !placed:
		x = sideX(t.fr, wa, pw, int32(10*sc+0.5), m)
		y = t.anchor.Y - ph/2
	}
	// 按窗口现在所在的那块显示器夹住（钉在扩展屏上的窗口不能被拉回主屏；那块屏拔掉了就落到最近的一块上）
	wa = monitorWork(point{x + pw/2, y + 20})
	x = max(wa.Left+m, min(x, wa.Right-pw-m))
	y = max(wa.Top+m, min(y, wa.Bottom-ph-m))
	flags := uintptr(swpNoActivate)
	if !placed {
		flags |= 0x40 // SWP_SHOWWINDOW
	}
	pSetWindowPos.Call(h, ^uintptr(0), uintptr(x), uintptr(y), uintptr(pw), uintptr(ph), flags) // HWND_TOPMOST
	t.mu.Lock()
	t.placed = true
	t.mu.Unlock()
	if !placed {
		f.pinSet(pinRec{SID: t.sid, X: x, Y: y, Has: true})
	}
}

// savePos 拖完以后记住新位置
func (t *taskWin) savePos() {
	t.mu.Lock()
	h := t.hwnd
	t.mu.Unlock()
	if h == 0 {
		return
	}
	var r rect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	t.f.pinSet(pinRec{SID: t.sid, X: r.Left, Y: r.Top, Has: true})
}

// drag 标题栏拖动：和悬浮窗一样由宿主按真实光标挪窗口（WebView2 在指针捕获期间给的坐标靠不住）
func (t *taskWin) drag() {
	t.mu.Lock()
	h := t.hwnd
	t.mu.Unlock()
	if h == 0 {
		return
	}
	var start, cur point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&start)))
	var r rect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	deadline := time.Now().Add(2 * time.Minute)
	for !t.dragEnd.Load() && time.Now().Before(deadline) {
		time.Sleep(8 * time.Millisecond)
		pGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
		pSetWindowPos.Call(h, 0, uintptr(r.Left+cur.X-start.X), uintptr(r.Top+cur.Y-start.Y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
	}
	t.savePos()
}
