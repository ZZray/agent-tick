package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDesktopSettingsReadAndWhitelist(t *testing.T) {
	h := newHarness(t, t.TempDir())
	h.srv.desktop = true
	var calls atomic.Int32
	owner := desktopSettingsHandler(func(ctx context.Context, in *desktopSettingsInput) (any, func(), error) {
		calls.Add(1)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("settings calls must have a bounded owner wait")
		}
		return map[string]any{"sound": "actual-owner-state", "autostart": true}, nil, nil
	})
	h.srv.settings.Store(&owner)
	if state := h.ok("GET", "/api/desktop/settings", nil); state["sound"] != "actual-owner-state" || state["autostart"] != true {
		t.Fatalf("GET must return the owner's state: %v", state)
	}
	for _, body := range []map[string]any{
		{"name": "float", "on": true}, {"name": "dnd", "on": false}, {"name": "autostart", "on": true},
		{"name": "sound", "val": ""}, {"name": "toastlen", "val": "long"}, {"name": "lang", "val": "en"}, {"name": "theme", "val": "blue"},
	} {
		if state := h.ok("POST", "/api/desktop/settings", body); state["sound"] != "actual-owner-state" {
			t.Fatalf("POST must return confirmed owner state, not echo the request: %v", state)
		}
	}
	if calls.Load() != 8 {
		t.Fatalf("owner calls: %d", calls.Load())
	}
	for _, body := range []map[string]any{
		{}, {"name": "quit"}, {"name": "update"}, {"name": "pin", "on": true},
		{"name": "dnd"}, {"name": "dnd", "on": "true"}, {"name": "float", "val": "true"},
		{"name": "sound"}, {"name": "sound", "val": 1}, {"name": "lang", "val": "en", "on": true},
	} {
		if code, _, _ := h.do("POST", "/api/desktop/settings", body); code != 400 {
			t.Fatalf("invalid settings request accepted: %v => %d", body, code)
		}
	}
	if calls.Load() != 8 {
		t.Fatal("invalid requests reached the desktop owner")
	}
	if code, _, _ := h.do("POST", "/api/desktop/settings", map[string]any{"name": "dnd", "on": true}, "Origin", "https://elsewhere.example"); code != 403 {
		t.Fatalf("settings writes must retain Origin guarding, got %d", code)
	}
}

func TestDesktopSettingsUnavailableAndOwnerErrors(t *testing.T) {
	h := newHarness(t, t.TempDir())
	for _, desktop := range []bool{false, true} {
		h.srv.desktop = desktop
		for _, method := range []string{"GET", "POST"} {
			if code, _, _ := h.do(method, "/api/desktop/settings", map[string]any{"name": "dnd", "on": true}); code != 503 {
				t.Fatalf("headless/unready settings must be 503, got %d", code)
			}
		}
	}
	var after atomic.Bool
	owner := desktopSettingsHandler(func(context.Context, *desktopSettingsInput) (any, func(), error) {
		return nil, func() { after.Store(true) }, errors.New("controlled settings write failure")
	})
	h.srv.settings.Store(&owner)
	if code, _, _ := h.do("POST", "/api/desktop/settings", map[string]any{"name": "theme", "val": "blue"}); code != 500 {
		t.Fatalf("owner errors must not become success: %d", code)
	}
	if after.Load() {
		t.Fatal("an unsuccessful setting must not execute post-reply effects")
	}
}

type settingsFlushFailure struct{ *httptest.ResponseRecorder }

func (*settingsFlushFailure) FlushError() error { return errors.New("controlled flush failure") }

func TestDesktopSettingsRestartRunsOnlyAfterFlushedReply(t *testing.T) {
	h := newHarness(t, t.TempDir())
	h.srv.desktop = true
	for _, failFlush := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		called := false
		owner := desktopSettingsHandler(func(context.Context, *desktopSettingsInput) (any, func(), error) {
			return map[string]any{"restart": true, "lang": "en", "effectiveLang": "en"}, func() {
				called = true
				if !recorder.Flushed || !strings.Contains(recorder.Body.String(), `"restart":true`) {
					t.Error("restart ran before the response was written and flushed")
				}
			}, nil
		})
		h.srv.settings.Store(&owner)
		req := httptest.NewRequest(http.MethodPost, h.base+"/api/desktop/settings", strings.NewReader(`{"name":"lang","val":"en"}`))
		req.Header.Set("Content-Type", "application/json")
		var writer http.ResponseWriter = recorder
		if failFlush {
			writer = &settingsFlushFailure{recorder}
		}
		h.srv.Handler().ServeHTTP(writer, req)
		if recorder.Code != 200 || called == failFlush {
			t.Fatalf("reply/restart sequence: failedFlush=%t status=%d restart=%t", failFlush, recorder.Code, called)
		}
	}
}
