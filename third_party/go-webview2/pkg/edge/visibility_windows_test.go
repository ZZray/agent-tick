//go:build windows

// Author: Ray Zhang.
package edge

import (
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The callback supplies independent COM and Win32 results without creating a UI.
var visibilityTestHRESULT, visibilityTestLastError, visibilityTestArgument uintptr
var visibilitySetLastError = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetLastError")
var visibilityTestProc = NewComProc(func(_ uintptr, visible uintptr) uintptr {
	visibilityTestArgument = visible
	visibilitySetLastError.Call(visibilityTestLastError)
	return visibilityTestHRESULT
})

func TestPutIsVisibleUsesHRESULT(t *testing.T) {
	if err := visibilitySetLastError.Find(); err != nil {
		t.Fatal(err)
	}
	controller := &ICoreWebView2Controller{vtbl: &_ICoreWebView2ControllerVtbl{PutIsVisible: visibilityTestProc}}
	for _, tc := range []struct {
		name          string
		hr, lastError uintptr
		visible       bool
		wantError     bool
	}{
		{"success ignores stale Win32 error", 0, 5, true, false},
		{"HRESULT failure with zero Win32 error", 0x80004005, 0, false, true},
		{"nonnegative HRESULT succeeds", 1, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			visibilityTestHRESULT, visibilityTestLastError = tc.hr, tc.lastError
			// Prove the stub really exposes the specified independent LastError.
			hr, _, lastError := visibilityTestProc.Call(uintptr(unsafe.Pointer(controller)), 1)
			if hr != tc.hr || lastError != syscall.Errno(tc.lastError) {
				t.Fatalf("native stub returned HRESULT=%x LastError=%v", hr, lastError)
			}
			err := controller.PutIsVisible(tc.visible)
			if (err != nil) != tc.wantError {
				t.Fatalf("PutIsVisible returned %v; wantError=%v", err, tc.wantError)
			}
			if tc.wantError && !strings.Contains(err.Error(), "80004005") {
				t.Fatalf("missing HRESULT: %v", err)
			}
			if visibilityTestArgument != uintptr(boolToInt(tc.visible)) {
				t.Fatalf("wrong BOOL argument: %d", visibilityTestArgument)
			}
		})
	}
}
