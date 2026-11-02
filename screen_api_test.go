package main

import "testing"

func TestScreenDesktopToggle(t *testing.T) {
	h := newHarness(t, t.TempDir())
	code, _, _ := h.do("POST", "/api/desktop/screen", map[string]bool{"on": true})
	if code != 404 {
		t.Fatalf("headless screen request: got %d, want 404", code)
	}
	var calls []bool
	h.srv.setScreen = func(on bool) { calls = append(calls, on) }
	for _, on := range []bool{true, false, true} {
		m := h.ok("POST", "/api/desktop/screen", map[string]bool{"on": on})
		if m["ok"] != true {
			t.Fatalf("toggle %v did not report acceptance: %v", on, m)
		}
	}
	if len(calls) != 3 || !calls[0] || calls[1] || !calls[2] {
		t.Fatalf("toggle callbacks: %v", calls)
	}
	code, _, _ = h.do("POST", "/api/desktop/screen", map[string]string{"on": "yes"})
	if code != 400 || len(calls) != 3 {
		t.Fatalf("invalid toggle reached desktop: status=%d calls=%v", code, calls)
	}
}
