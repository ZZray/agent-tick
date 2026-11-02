# 原生悬浮窗清单展开：预留 WebView 视口

日期：2026-10-31。基于 `3207698`，分支 `kiro/fold-motion-20261031`。用户在隔离预览中确认观感，尚未合入或发布。

## 现象与根因

展开清单时，窗口新长出的一截先是灰色（浅色桌面上偏白），下一帧才出现内容。页面的 ResizeObserver 逐帧调 `tickFit`，宿主 `fit()` 调 `SetWindowPos`。go-webview2 在 WM_SIZE 中调 `browser.Resize()`，把 WebView2 视口设为窗口客户区大小。新视口要等渲染进程再出一帧，期间只露出 DWM Acrylic 底色。

内容层的 `.pad` 原先 60ms 延迟淡入，高度曲线是强 ease-out，空框会先于内容到位。

## 做法

- `reserve_windows.go`：`tickReserve(on, cssH)`。开启时通过反射拿 go-webview2 私有字段 `browser`（`*edge.Chromium`），取控制器后直接调 `put_Bounds`（vtable 第 7 项）把视口撑到目标高度。预留期间 `floatProc` 截住 `WM_SIZE` 并保持视口高度，不交给库处理。关闭时把视口恢复成客户区大小。
- 预留期间 `fit()` 使用开始时记下的生长方向 `resUp`，窗口中心越过屏幕中线时不换锚边。
- `web/index.html`：`case 'toggle'` 先 `await foldReserve()`，再切换 `.open`，然后 `foldSlide()`。`foldReserve` 在开始前量出目标高度，设置 `data-reserving`，在 `FOLD_MS + 150ms` 后撤销；连续点击时重新计时。
- 参数：`--t-fold: 320ms`，`--ease-fold: cubic-bezier(.33,.1,.2,1)`；内容淡入 120ms，没有延迟；子项从右侧 18px 滑入，220ms，每行错开 16ms，最多错开 8 档。

## 陷阱

- go-webview2 没有导出控制器。升级依赖时，若私有字段名 `browser` 或 vtable 顺序变化，`controller()` 会返回 nil，功能退回原来的逐帧调整，不会崩溃，但灰区会回来。
- `go vet` 不接受 `unsafe.Pointer(*(*uintptr)(...))` 这种写法，读取 vtable 要写成 `**(**[7]uintptr)(unsafe.Pointer(c))`。
- 60fps 的 gdigrab 录屏会漏帧，GIF 的帧间隔也不能当作应用帧率。验收方法是真实点击，同时以 1ms 间隔轮询 `GetWindowRect`，再用 `CopyFromScreen` 连拍（`.shots/anim-track.ps1`、`.shots/anim-burst.ps1`）。

## 验证与未覆盖

真实点击下，展开约 310ms，窗口约每 16ms 增高一步，顶边不动；连拍 30 帧未见灰白新增区。go vet、go test 和 Node 回归均已通过。未覆盖贴边停靠、跨屏、非 100% DPI 和浅色主题。

## 通知的签名动效（同一分支）

用户不要“边缘扩散”，要“画一个圈再打勾/打叉”。提醒卡图标改成 `.sg` 结构：底色圆 `.sg-fill`、描边圆 `.sg-ring`（`pathLength=1`，`rotate(-90 15 15)`）和标记 `.sg-mk`，动画全在 CSS。删掉 `tc-burst` 和提醒卡的 `edgeBeam`。悬浮窗 `noticeGlow(kind, sid)` 不再让外框绕光，改为 `iconSig()` 在该会话的状态图标上叠一个临时圆（带 `data-fx`，morph 不会当成正式节点）并用 WAAPI 播放；选择器覆盖 `.s`、`.kid`、`.mini` 和胶囊 `pill-<sid>`。通知到达后等 80ms 再加一帧，确保状态图标已经换成新状态。

尚未覆盖：贴边标签和长条形态没有会话图标，目前这两种形态下没有提示；抽屉里等你/受阻卡片的周期绕光（`pulse` 中的 `.n-card`）保持原样。
