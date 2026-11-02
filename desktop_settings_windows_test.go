//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

func TestAutoArchiveSettingDefaultsPersistsAndRollsBackOnError(t *testing.T) {
	tr, _ := settingsTestTray(t)
	if tr.f.set.AutoArchive || tr.state().AutoArchive {
		t.Fatal("auto archive must default off")
	}
	if _, err := tr.applySetting("autoarchive", true, ""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(tr.f.file)
	if err != nil {
		t.Fatal(err)
	}
	var restored deskSettings
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.AutoArchive || !tr.state().AutoArchive {
		t.Fatal("enabled state not persisted and exposed")
	}
	tr.f.file = t.TempDir()
	if _, err := tr.applySetting("autoarchive", false, ""); err == nil {
		t.Fatal("settings write error was swallowed")
	}
	if !tr.f.set.AutoArchive {
		t.Fatal("failed write changed enabled preference")
	}
}

func TestNotificationDisplaySettingsPersistIndependentlyAndRollback(t *testing.T) {
	tr, _ := settingsTestTray(t)
	if !tr.state().TrayFlash || !tr.state().NotificationBadge {
		t.Fatal("notification display defaults changed")
	}
	for _, name := range []string{"trayflash", "notificationbadge"} {
		if _, err := tr.applySetting(name, false, ""); err != nil {
			t.Fatal(err)
		}
	}
	if tr.state().TrayFlash || tr.state().NotificationBadge {
		t.Fatal("display toggles did not apply")
	}
	b, err := os.ReadFile(tr.f.file)
	if err != nil {
		t.Fatal(err)
	}
	var saved deskSettings
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.NoTrayFlash || !saved.NoBadge || saved.Sound != "" || saved.DndUntil != 0 {
		t.Fatal("display settings changed sound or do-not-disturb")
	}
	tr.f.file = t.TempDir()
	for _, name := range []string{"trayflash", "notificationbadge"} {
		if _, err := tr.applySetting(name, true, ""); err == nil {
			t.Fatal("failed save reported success")
		}
	}
	if tr.state().TrayFlash || tr.state().NotificationBadge {
		t.Fatal("failed save changed effective state")
	}
}

// Only Dispatch is used. No WebView, sound playback, registry write, or restart runs.
type settingsQueueView struct {
	webview2.WebView
	queue chan func()
}

func (w *settingsQueueView) Dispatch(fn func()) { w.queue <- fn }

func settingsTestTray(t *testing.T) (*tray, *settingsQueueView) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := &settingsQueueView{queue: make(chan func(), 16)}
	f := &floatWin{w: w, file: filepath.Join(dir, "desktop.json")}
	return &tray{f: f, srv: &Server{store: store}, up: &updater{f: f}}, w
}

func TestDesktopSettingsWaitsForOwnerAndReturnsAppliedState(t *testing.T) {
	tr, view := settingsTestTray(t)
	val := "long"
	type result struct {
		state any
		err   error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		state, _, err := tr.settings(ctx, &desktopSettingsInput{Name: "toastlen", Val: &val})
		done <- result{state, err}
	}()
	var apply func()
	select {
	case apply = <-view.queue:
	case <-ctx.Done():
		t.Fatal("owner work was not queued")
	}
	select {
	case <-done:
		t.Fatal("settings returned before owner work completed")
	default:
	}
	apply()
	select {
	case r := <-done:
		if r.err != nil || r.state.(trayState).ToastLen != "long" {
			t.Fatalf("did not return applied owner state: %+v", r)
		}
	case <-ctx.Done():
		t.Fatal("owner completion did not resolve the request")
	}
}

func TestDesktopSettingsCancelledQueuedWorkDoesNotApply(t *testing.T) {
	tr, view := settingsTestTray(t)
	val := "long"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := tr.settings(ctx, &desktopSettingsInput{Name: "toastlen", Val: &val})
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 503 {
		t.Fatalf("unconfirmed owner request must be 503: %v", err)
	}
	(<-view.queue)()
	if tr.f.set.ToastLen != "" {
		t.Fatal("cancelled queued work changed settings later")
	}
}

func TestDesktopSettingsRejectsUnsupportedValuesAndReportsSaveErrors(t *testing.T) {
	tr, _ := settingsTestTray(t)
	for _, in := range []struct{ name, val string }{
		{"sound", "file:../no.wav"}, {"toastlen", "forever"}, {"theme", "unknown"}, {"lang", "xx"}, {"quit", ""},
	} {
		if _, err := tr.applySetting(in.name, false, in.val); err == nil {
			t.Fatalf("accepted unsupported settings value: %+v", in)
		}
	}
	tr.f.file = t.TempDir() // writing settings to a directory must fail, without touching installed data.
	if _, err := tr.applySetting("toastlen", false, "long"); err == nil {
		t.Fatal("settings persistence failure was swallowed")
	}
	if _, err := tr.applySetting("sound", false, ""); err == nil {
		t.Fatal("silent sound setting swallowed persistence failure")
	}
	if _, err := tr.applySetting("dnd", false, ""); err == nil {
		t.Fatal("DND setting swallowed persistence failure")
	}
}
