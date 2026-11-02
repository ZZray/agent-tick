//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFloatWidthAbsoluteCursorAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		drag   floatWidthDrag
		cursor int32
		want   float64
	}{
		{"right grows", floatWidthDrag{side: "right", origin: rect{100, 50, 440, 210}, work: rect{0, 0, 1920, 1080}, startX: 440, scale: 1}, 500, 400},
		{"left grows with fixed right", floatWidthDrag{side: "left", origin: rect{500, 50, 840, 210}, work: rect{0, 0, 1920, 1080}, startX: 500, scale: 1}, 440, 400},
		{"DPI scales delta", floatWidthDrag{side: "right", origin: rect{100, 50, 780, 370}, work: rect{0, 0, 2560, 1440}, startX: 780, scale: 2}, 900, 400},
		{"minimum", floatWidthDrag{side: "right", origin: rect{100, 50, 440, 210}, work: rect{0, 0, 1920, 1080}, startX: 440, scale: 1}, 0, 320},
		{"maximum", floatWidthDrag{side: "right", origin: rect{100, 50, 440, 210}, work: rect{0, 0, 1920, 1080}, startX: 440, scale: 1}, 2000, 720},
		{"space to right edge", floatWidthDrag{side: "right", origin: rect{1500, 50, 1840, 210}, work: rect{0, 0, 1920, 1080}, startX: 1840, scale: 1}, 2100, 420},
		{"space to left edge", floatWidthDrag{side: "left", origin: rect{100, 50, 440, 210}, work: rect{0, 0, 1920, 1080}, startX: 100, scale: 1}, -200, 440},
		{"tiny work area", floatWidthDrag{side: "right", origin: rect{0, 50, 250, 210}, work: rect{0, 0, 250, 1080}, startX: 250, scale: 1}, 400, 250},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.drag.widthAt(tc.cursor); got != tc.want {
				t.Fatalf("width=%g want=%g", got, tc.want)
			}
			// A repeated captured position must not accumulate another delta.
			if got := tc.drag.widthAt(tc.cursor); got != tc.want {
				t.Fatalf("same cursor changed width: %g", got)
			}
		})
	}
}

func TestFloatWidthKeepsOppositeAndVerticalEdges(t *testing.T) {
	d := floatWidthDrag{side: "left", origin: rect{500, 400, 840, 560}, up: true}
	for _, h := range []int32{100, 700} {
		x, y := d.position(500, h)
		if x+500 != d.origin.Right || y+h != d.origin.Bottom {
			t.Fatalf("left/up anchor changed after reflow: x=%d y=%d h=%d", x, y, h)
		}
	}
	d.side, d.up = "right", false
	x, y := d.position(600, 700)
	if x != d.origin.Left || y != d.origin.Top {
		t.Fatal("right/top anchor changed")
	}
}

func TestFloatWidthActiveGestureTakesPriorityOverRecentStripAnchor(t *testing.T) {
	f := &floatWin{}
	f.stripAnchorUntil.Store(now() + 60_000)
	if !f.useStripAnchor(false) || f.useStripAnchor(true) {
		t.Fatal("ordinary strip anchor behavior changed")
	}
	f.widthDrag = &floatWidthDrag{side: "left", origin: rect{500, 100, 840, 260}}
	if f.useStripAnchor(false) {
		t.Fatal("a recent strip anchor must not override the active resize")
	}
	x, y := f.widthDrag.position(500, 200)
	if x+500 != 840 || y != 100 {
		t.Fatal("active resize lost its fixed edge")
	}
	f.widthDrag = nil
	if !f.useStripAnchor(false) {
		t.Fatal("finishing resize did not restore the remaining strip-anchor interval")
	}
}

func TestFloatWidthStartsWithExistingReserveGrowthDirection(t *testing.T) {
	f := &floatWin{}
	work := rect{0, 0, 1000, 1000}
	upper, lower := rect{0, 100, 340, 300}, rect{0, 700, 340, 900}
	if f.resizeUp(upper, work) || !f.resizeUp(lower, work) {
		t.Fatal("ordinary initial growth direction changed")
	}
	f.resH = 720
	f.resUp = true
	if !f.resizeUp(upper, work) {
		t.Fatal("active upward reserve was replaced by the current midpoint")
	}
	f.resUp = false
	if f.resizeUp(lower, work) {
		t.Fatal("active downward reserve was replaced by the current midpoint")
	}
}

func TestFloatWidthDefaultAndWorkAreaClipDoNotRewritePreference(t *testing.T) {
	f := &floatWin{}
	if f.preferredWidth() != 340 {
		t.Fatal("old settings must retain default width")
	}
	f.set.Width = 600
	if got := boundedFloatWidth(f.preferredWidth(), 250); got != 250 {
		t.Fatalf("small work area was not allowed below 320: %g", got)
	}
	if f.set.Width != 600 || boundedFloatWidth(f.preferredWidth(), 1000) != 600 {
		t.Fatal("a temporary work-area clip replaced the preference")
	}
}

func TestFloatWidthModeGateUsesPresentationRatherThanCompactPreference(t *testing.T) {
	f := &floatWin{}
	f.set.Compact = true // Dock expansion can present the full panel without changing this preference.
	if err := f.checkWidthResize("right"); err != nil {
		t.Fatalf("compact preference incorrectly rejected the full-panel grip: %v", err)
	}
	f.set.Dock = "right"
	if f.checkWidthResize("right") == nil || f.checkWidthResize("left") != nil {
		t.Fatal("only the docked edge must be unavailable")
	}
	f.set.Dock = ""
	if f.checkWidthResize("diagonal") == nil {
		t.Fatal("an unknown grip must be rejected")
	}
	f.tucked.Store(true)
	if f.checkWidthResize("right") == nil {
		t.Fatal("a tucked presentation must be rejected")
	}
	f.tucked.Store(false)
	f.anchorOn = true
	if f.checkWidthResize("right") == nil {
		t.Fatal("a fan presentation must be rejected")
	}
	f.anchorOn = false
	f.widthDrag = &floatWidthDrag{}
	if f.checkWidthResize("right") == nil {
		t.Fatal("a second active resize must be rejected")
	}
}

func TestFloatWidthFinishSavesWidthAndPositionOnce(t *testing.T) {
	f := &floatWin{file: filepath.Join(t.TempDir(), "desktop.json")}
	f.set.Sound, f.set.Compact = "bell", false
	d := &floatWidthDrag{origin: rect{100, 50, 440, 210}, width: 500, scale: 1, moved: true, timer: time.NewTimer(time.Hour)}
	if !dragging.TryLock() {
		t.Fatal("dragging lock unavailable")
	}
	f.widthDrag = d
	isDragging.Store(true)
	t.Cleanup(func() {
		if f.widthDrag != nil {
			f.widthDrag = nil
			isDragging.Store(false)
			dragging.Unlock()
		}
	})
	if width, err := f.finishWidthDrag(d, rect{80, 70, 580, 350}, nil); err != nil || width != 500 {
		t.Fatalf("finish: width=%g err=%v", width, err)
	}
	if f.widthDrag != nil || isDragging.Load() || d.timer.Stop() {
		t.Fatal("finish did not release active state or stop its deadline")
	}
	if !dragging.TryLock() {
		t.Fatal("finish left dragging locked")
	}
	dragging.Unlock()
	b, err := os.ReadFile(f.file)
	if err != nil {
		t.Fatal(err)
	}
	var saved deskSettings
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Width != 500 || saved.X != 80 || saved.Y != 70 || !saved.HasPos || saved.Sound != "bell" {
		t.Fatalf("final settings lost width, position or unrelated preferences: %+v", saved)
	}
	// If an old end/timer callback runs again, it must neither save nor release a newer gesture.
	if err := os.Remove(f.file); err != nil {
		t.Fatal(err)
	}
	if _, err := f.finishWidthDrag(d, rect{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a repeated end saved again")
	}
}

func TestFloatWidthFinishPreservesClippedPreferenceAndReleasesOnSaveFailure(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "clip", true: "save failure"}[failSave], func(t *testing.T) {
			dir := t.TempDir()
			f := &floatWin{file: filepath.Join(dir, "desktop.json")}
			f.set.Width = 600
			if failSave {
				f.file = dir
			}
			d := &floatWidthDrag{origin: rect{0, 0, 250, 300}, width: 250, scale: 1, moved: true}
			if !dragging.TryLock() {
				t.Fatal("dragging lock unavailable")
			}
			f.widthDrag = d
			isDragging.Store(true)
			_, err := f.finishWidthDrag(d, d.origin, nil)
			if (err != nil) != failSave || f.set.Width != 600 || f.widthDrag != nil || isDragging.Load() {
				t.Fatalf("finish did not preserve the preference or release on error: err=%v state=%+v", err, f)
			}
			if !dragging.TryLock() {
				t.Fatal("save failure left dragging locked")
			}
			dragging.Unlock()
		})
	}
}

func TestFloatWidthOldCompletionCannotReleaseNewGestureAndStepFailureStillReleases(t *testing.T) {
	f := &floatWin{file: filepath.Join(t.TempDir(), "desktop.json")}
	f.set.Width = 500
	old := &floatWidthDrag{width: 400, scale: 1}
	active := &floatWidthDrag{width: 600, scale: 1, moved: true}
	if !dragging.TryLock() {
		t.Fatal("dragging lock unavailable")
	}
	f.widthDrag = active
	isDragging.Store(true)
	t.Cleanup(func() {
		if f.widthDrag != nil {
			f.widthDrag = nil
			isDragging.Store(false)
			dragging.Unlock()
		}
	})
	if _, err := f.finishWidthDrag(old, rect{}, nil); err != nil || f.widthDrag != active || !isDragging.Load() {
		t.Fatal("stale completion affected the active gesture")
	}
	if _, err := os.Stat(f.file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale completion saved settings")
	}
	stepErr := errors.New("controlled final cursor/position failure")
	if _, err := f.finishWidthDrag(active, rect{80, 70, 580, 350}, stepErr); !errors.Is(err, stepErr) {
		t.Fatalf("final step failure was swallowed: %v", err)
	}
	if f.set.Width != 500 || f.widthDrag != nil || isDragging.Load() {
		t.Fatal("failed native step changed the preference or retained ownership")
	}
	if !dragging.TryLock() {
		t.Fatal("step error left dragging locked")
	}
	dragging.Unlock()
}
