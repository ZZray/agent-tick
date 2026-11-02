# Tick local WebView2 patch

Local patch maintainer: Ray Zhang.

Source: https://github.com/jchv/go-webview2/tree/56598839c808

Pinned module version: `v0.0.0-20260205173254-56598839c808`.
The 50 upstream files were copied from the verified Go module cache, including
`LICENSE` and `webviewloader/sdk/LICENSE.txt`. All 49 files other than
`webview.go` remain byte-for-byte identical to that version.
The scoped `.gitattributes` rule preserves these bytes across checkouts.

The only upstream code change adds `WindowOptions.Hidden`. Its default is false.
When true, initial creation skips `ShowWindow`, `UpdateWindow`, and `SetFocus`.
Tick sets it only for the tray settings submenu, then positions and displays that
window after its content is ready. The other Tick windows keep the default.

The root `go.mod` selects this copy with a local `replace`. To update it, compare
all files against the new pinned upstream module, reapply or remove this small
patch, retain both licenses, and verify native submenu creation, keyboard focus,
and the existing windows before releasing.
