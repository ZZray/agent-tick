//go:build windows

package main

// 清单展开 / 收起时窗口逐帧变高。go-webview2 在每次 WM_SIZE 里把 WebView 视口改成窗口大小，
// 新视口要等浏览器重新出一帧才有内容，这一两帧里新长出来的那截只有系统 Acrylic 底色（深色发灰、浅色桌面上发白）。
// 做法：动画开始前先把 WebView 视口一次撑到够大（窗口本身不变），动画期间 WM_SIZE 不再改视口，
// 窗口变高只是把已经画好的那部分露出来；动画结束再把视口收回窗口大小。

import (
	"math"
	"reflect"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
)

// controller 取 go-webview2 内部的 WebView2 控制器（库没有导出，用反射读私有字段 browser）
func (f *floatWin) controller() *edge.ICoreWebView2Controller {
	if f.ctl != nil {
		return f.ctl
	}
	v := reflect.ValueOf(f.w)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	b := v.Elem().FieldByName("browser")
	if !b.IsValid() {
		return nil
	}
	c, _ := reflect.NewAt(b.Type(), unsafe.Pointer(b.UnsafeAddr())).Elem().Interface().(*edge.Chromium)
	if c == nil {
		return nil
	}
	f.ctl = c.GetController()
	return f.ctl
}

// putBounds 直接调 ICoreWebView2Controller::put_Bounds（vtable 第 7 项：IUnknown 三项、Get/PutIsVisible、GetBounds 之后）
func (f *floatWin) putBounds(w, h int32) {
	c := f.controller()
	if c == nil {
		return
	}
	r := rect{0, 0, w, h}
	vt := **(**[7]uintptr)(unsafe.Pointer(c)) // COM 对象第一个字段是 vtable 指针
	syscall.SyscallN(vt[6], uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(&r)))
}

func (f *floatWin) clientSize() (int32, int32) {
	var r rect
	pGetClientRect.Call(f.hwnd, uintptr(unsafe.Pointer(&r)))
	return r.Right - r.Left, r.Bottom - r.Top
}

// reserve 页面在清单展开 / 收起前调用：on 时把视口撑到 cssH 高（CSS 像素，不小于当前窗口），
// 并记住窗口现在朝哪边长，动画中途窗口中心越过屏幕中线也不换方向；off 时视口收回窗口大小
func (f *floatWin) reserve(on bool, cssH float64) {
	if !on {
		if f.resH == 0 {
			return
		}
		f.resH = 0
		f.putBounds(f.clientSize())
		return
	}
	if f.resH == 0 {
		old, wa := f.windowRect(), f.workArea()
		f.resUp = (old.Top+old.Bottom)/2 > (wa.Top+wa.Bottom)/2
	}
	w, h := f.clientSize()
	f.resH = max(h, int32(math.Round(math.Min(cssH, 720)*f.scale())))
	f.putBounds(w, f.resH)
}

// onSize 窗口尺寸变了：预留期间视口保持撑开的高度，返回 true 表示已处理（不交给 go-webview2 改视口）
func (f *floatWin) onSize() bool {
	if f.resH == 0 {
		return false
	}
	w, h := f.clientSize()
	f.resH = max(f.resH, h)
	f.putBounds(w, f.resH)
	return true
}
