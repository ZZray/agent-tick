# Tick local WebView2 patch

Local patch maintainer: Ray Zhang.

Source: https://github.com/jchv/go-webview2/tree/56598839c808

Pinned module version: `v0.0.0-20260205173254-56598839c808`.
The 50 upstream files were copied from the verified Go module cache, including
`LICENSE` and `webviewloader/sdk/LICENSE.txt`. All 48 files other than
`webview.go` and `pkg/edge/ICoreWebView2Controller.go` remain byte-for-byte identical
to that version.
The scoped `.gitattributes` rule preserves these bytes across checkouts.

`WindowOptions.Hidden` defaults to false. When true, initial creation skips
`ShowWindow`, `UpdateWindow`, and `SetFocus`. After embedding, it calls the existing
controller `Show` method while the parent HWND remains hidden. A controller
created under a hidden parent otherwise starts invisible and leaves an empty
popup when the parent is shown. Tick enables this path only for the tray settings
submenu; the other windows keep the default.

`PutIsVisible`, used by that `Show` call, now checks the signed 32-bit COM HRESULT
instead of Win32 `LastError`. Its regression tests use a native callback to supply
independent HRESULT and LastError values without opening a window. Run them with
`go test github.com/jchv/go-webview2/pkg/edge`; the root release workflow includes
this command because root `go test ./...` does not traverse the nested module.

The root `go.mod` selects this copy with a local `replace`. To update it, compare
all files against the new pinned upstream module, reapply or remove this small
patch, retain both licenses, and verify native submenu creation, keyboard focus,
and the existing windows before releasing.
