//go:build windows

package main

// 毛玻璃：Win11 22H2（build 22621）起，DWM 可以给窗口一层系统 Acrylic 背景（背后内容的高斯模糊 + 着色）。
// 做法：WebView2 默认背景设成全透明（环境变量，必须在建第一个 WebView 之前设），
// 窗口把框架扩展到整个客户区，再把背景类型设成 Acrylic；页面用半透明底色叠在上面。
// 更早的系统不支持，保持原来的不透明背景（页面不加 data-glass）。

import (
	"os"
	"sync"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	pDwmExtendFrame = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
	glassOn         bool
)

const (
	dwmaDarkMode       = 20
	dwmaSystemBackdrop = 38
	backdropAcrylic    = 3 // DWMSBT_TRANSIENTWINDOW
)

// initGlass 在建任何 WebView 之前调用；环境变量 TICK_NO_GLASS=1 可以关掉
func initGlass() {
	glassOn = os.Getenv("TICK_NO_GLASS") != "1" && windows.RtlGetVersion().BuildNumber >= 22621
}

var glassEnvMu sync.Mutex

// newGlassWebView 建一个透明底的 WebView：透明底只能通过环境变量在建的那一刻指定，
// 所以只在建悬浮窗、提醒卡、托盘菜单时临时设上，建完就撤掉，
// 免得面板窗口和 Tick 启动的其他程序（更新重启、打开链接）也继承到
func newGlassWebView(o webview2.WebViewOptions) webview2.WebView {
	if !glassOn {
		return webview2.NewWithOptions(o)
	}
	glassEnvMu.Lock()
	defer glassEnvMu.Unlock()
	_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "00000000")
	defer os.Unsetenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR")
	return webview2.NewWithOptions(o)
}

// applyGlass 给一个窗口开系统 Acrylic 背景；dark 决定着色跟深色还是浅色走
func applyGlass(h uintptr, dark bool) {
	if !glassOn {
		return
	}
	m := [4]int32{-1, -1, -1, -1}
	pDwmExtendFrame.Call(h, uintptr(unsafe.Pointer(&m)))
	kind := int32(backdropAcrylic)
	pDwmSetWindowAttribute.Call(h, dwmaSystemBackdrop, uintptr(unsafe.Pointer(&kind)), 4)
	setDarkFrame(h, dark)
}

func setDarkFrame(h uintptr, dark bool) {
	v := int32(0)
	if dark {
		v = 1
	}
	pDwmSetWindowAttribute.Call(h, dwmaDarkMode, uintptr(unsafe.Pointer(&v)), 4)
}
