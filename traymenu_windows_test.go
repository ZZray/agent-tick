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
