//go:build windows

package main

import "testing"

func TestMenuBounds(t *testing.T) {
	cases := []struct {
		name        string
		w, h, scale float64
		anchor      point
		work        rect
		want        rect
	}{
		{"bottom taskbar", 300, 320, 1, point{1900, 1060}, rect{0, 0, 1920, 1040}, rect{1610, 710, 1910, 1030}},
		{"expanded short monitor", 300, 900, 1, point{1260, 790}, rect{0, 0, 1280, 760}, rect{970, 10, 1270, 750}},
		{"top taskbar", 300, 320, 1, point{900, 30}, rect{0, 40, 1920, 1080}, rect{630, 50, 930, 370}},
		{"left monitor high DPI", 300, 900, 1.5, point{-20, 1060}, rect{-1920, 0, 0, 1040}, rect{-465, 15, -15, 1025}},
		{"width clamped", 300, 900, 1, point{180, 290}, rect{0, 0, 200, 280}, rect{10, 10, 190, 270}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := menuBounds(tc.w, tc.h, tc.scale, tc.anchor, tc.work); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	work := rect{0, 0, 1280, 760}
	anchor := point{1260, 790}
	expanded := menuBounds(300, 900, 1, anchor, work)
	collapsed := menuBounds(300, 320, 1, anchor, work)
	if collapsed.Bottom-collapsed.Top != 320 || collapsed.Bottom != expanded.Bottom {
		t.Fatalf("collapse should shrink against same work-area edge: %+v -> %+v", expanded, collapsed)
	}
}

func TestSubmenuBounds(t *testing.T) {
	cases := []struct {
		name         string
		parent, work rect
		scale        float64
		want         rect
	}{
		{"right", rect{100, 100, 400, 430}, rect{0, 0, 1920, 1080}, 1, rect{408, 380, 708, 880}},
		{"flip left", rect{1600, 700, 1900, 1030}, rect{0, 0, 1920, 1040}, 1, rect{1292, 530, 1592, 1030}},
		{"short work area", rect{600, 100, 900, 430}, rect{0, 0, 1280, 480}, 1, rect{908, 10, 1208, 470}},
		{"negative high DPI", rect{-1000, 100, -550, 595}, rect{-1920, 0, 0, 1080}, 1.5, rect{-538, 315, -88, 1065}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := submenuBounds(300, 500, tc.scale, 280, tc.parent, tc.work)
			if got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestSubmenuCancelBeforeWindowExists(t *testing.T) {
	parent := &menuWin{}
	sub := &menuWin{parent: parent, wantShow: true, wantFocus: true, opening: true, gen: 7}
	sub.hide()
	if sub.wantShow || sub.wantFocus || sub.gen != 8 {
		t.Fatalf("cancel left pending child activation: %+v", sub)
	}
}
