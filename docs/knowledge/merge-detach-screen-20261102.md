# 分离、大屏并入 0.5.15 之后

日期：2026-11-02。目标：把停在 `681b30b` 之上的未提交工作区并入 `origin/main`（`f51e296`，0.5.15）后发布。

## 为什么用户运行的版本没有分离

分离（`task_windows.go`）和大屏（`screen_windows.go`、`eventlog.go`）只存在于 0.5.13 之上的未提交工作区，没进过任何提交。0.5.14、0.5.15 由 `origin/main` 构建，已装的 `tick.exe` 里搜不到 `tickDetach`。以后新功能先提交到分支，再谈发布。

## 合并时的取舍

- 子任务折叠有两套实现：工作区里的"整组收起"（`S.kidsOpen`、`.kids-h`、`data-act="kids"`）和 `origin/main` 的"每个子任务各自展开"（`S.open`）。保留 `origin/main` 的版本，删掉整组收起的代码、样式和对应知识文档 `api-session-collapse-20261031.md`。折叠行为以 `checklist-folding.md` 为准。
- `toastlen` 已由 `origin/main` 移到 `applySetting`，托盘菜单只保留 `case "screen"`。
- `saveSettings` 采用 `origin/main` 的 `error` 返回版本，同时保留钉住任务的 `pinGet/pinSet/pinDel`。

## 测试切片边界

`web/*.test.cjs` 用 `html.indexOf(...)` 截取页面脚本片段放进 `vm`。大屏测试从 `const CV_GAP` 截到 `// 面板设置留在侧栏 morph 之外`，设置测试从 `const settings = {` 截到 `// ---------- 全屏卡片：`。两段不能重叠，移动这些注释前先看测试。`folding.test.cjs` 的沙箱需要 `window: {}`，会话行里有 `window.tickDetach` 判断。大屏测试文件改名为 `screen.test.cjs`，让 `node --test web/*.test.cjs` 能一起跑。