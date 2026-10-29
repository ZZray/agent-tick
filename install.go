package main

// tick install：一条命令装好。
//   1. 把 tick.exe 复制到固定位置（%LOCALAPPDATA%\Programs\Tick），配置都指向它，不依赖下载目录
//   2. 找出本机装了哪些 agent（看配置目录在不在），只给装了的配：
//      - skill：写一份 tick 技能（SKILL.md），可以在对话里直接 @tick 或让 agent 自己按描述加载
//      - MCP：用 stdio 方式（tick.exe mcp），服务没开时会自动拉起
//   3. 改任何已有配置文件前先备份成 *.bak-tick；同名配置已经在就跳过
// tick install --dry-run 只打印要做什么；tick uninstall 撤掉 skill 和 MCP 配置（不删数据）

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed skill/SKILL.md
var skillTemplate string

type agentTarget struct {
	name     string
	home     string // 存在才算装了
	skillDir string // 空表示这个 agent 没有 skill 机制
	mcp      func(exe string, dry, remove bool) (string, error)
}

func installTargets(home string) []agentTarget {
	j := func(p ...string) string { return filepath.Join(append([]string{home}, p...)...) }
	jsonMCP := func(file string) func(string, bool, bool) (string, error) {
		return func(exe string, dry, remove bool) (string, error) { return mergeJSONMCP(file, exe, dry, remove) }
	}
	return []agentTarget{
		{"Claude Code", j(".claude"), j(".claude", "skills", "tick"), claudeMCP},
		{"Codex", j(".codex"), j(".codex", "skills", "tick"), func(exe string, dry, remove bool) (string, error) {
			return codexMCP(j(".codex", "config.toml"), exe, dry, remove)
		}},
		{"Kiro", j(".kiro"), j(".kiro", "skills", "tick"), jsonMCP(j(".kiro", "settings", "mcp.json"))},
		{"Cursor", j(".cursor"), j(".cursor", "skills", "tick"), jsonMCP(j(".cursor", "mcp.json"))},
		{"Gemini CLI", j(".gemini"), "", jsonMCP(j(".gemini", "settings.json"))},
		{"通用 agents 目录", j(".agents"), j(".agents", "skills", "tick"), nil},
	}
}

func installDir() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" && runtime.GOOS == "windows" {
		return filepath.Join(d, "Programs", "Tick")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "tick")
}

func runInstall(args []string) int {
	dry, remove := false, args[0] == "uninstall"
	for _, a := range args[1:] {
		if a == "--dry-run" || a == "-n" {
			dry = true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "找不到用户目录：", err)
		return 1
	}
	say := func(format string, a ...any) { fmt.Printf(format+"\n", a...) }
	if dry {
		say("（预演：只列出要做的事，不改任何文件）")
	}

	// 1. 程序放到固定位置
	// 下载下来的文件名带平台后缀（tick-windows-amd64.exe），装好后统一叫 tick.exe
	exe, _ := os.Executable()
	target := filepath.Join(installDir(), "tick.exe")
	if !remove {
		if strings.EqualFold(filepath.Clean(exe), filepath.Clean(target)) {
			say("✓ 程序已在 %s", target)
		} else if dry {
			say("· 复制程序到 %s", target)
		} else if err := copyFile(exe, target); err != nil {
			if _, statErr := os.Stat(target); statErr == nil { // 多半是旧版正在运行，文件被占用
				say("· 没能覆盖 %s（%v），先沿用它；退出 Tick 后再装一次，或用 tick update 更新", target, err)
			} else {
				say("✗ 复制程序失败：%v（配置会指向当前位置 %s）", err, exe)
				target = exe
			}
		} else {
			say("✓ 程序已复制到 %s", target)
		}
	}

	// 2. 各个 agent
	found := 0
	for _, t := range installTargets(home) {
		if _, err := os.Stat(t.home); err != nil {
			continue
		}
		found++
		say("\n%s", t.name)
		if t.skillDir != "" {
			file := filepath.Join(t.skillDir, "SKILL.md")
			switch {
			case remove && dry:
				say("  · 删除技能 %s", t.skillDir)
			case remove:
				if err := os.RemoveAll(t.skillDir); err != nil {
					say("  ✗ 删除技能失败：%v", err)
				} else {
					say("  ✓ 已删除技能")
				}
			case dry:
				say("  · 写入技能 %s", file)
			default:
				body := strings.ReplaceAll(skillTemplate, "{{EXE}}", target)
				body = strings.ReplaceAll(body, "{{BASE}}", fmt.Sprintf("http://127.0.0.1:%d", defaultPort()))
				if err := os.MkdirAll(t.skillDir, 0o755); err == nil {
					err = os.WriteFile(file, []byte(body), 0o644)
				}
				if err != nil {
					say("  ✗ 写入技能失败：%v", err)
				} else {
					say("  ✓ 技能：%s", file)
				}
			}
		}
		if t.mcp != nil {
			msg, err := t.mcp(target, dry, remove)
			if err != nil {
				say("  ✗ MCP：%v", err)
			} else {
				say("  %s", msg)
			}
		}
	}
	if found == 0 {
		say("没找到常用 agent 的配置目录（.claude / .codex / .kiro / .cursor / .gemini / .agents）。")
	}
	if !remove && !dry {
		if !pingTick(defaultPort()) { // 装完顺手启动，托盘和悬浮窗马上出现
			cmd := exec.Command(target)
			hideConsole(cmd)
			if err := cmd.Start(); err != nil {
				say("\n✗ 启动 Tick 失败：%v，可以手动运行 %s", err, target)
			} else {
				_ = cmd.Process.Release()
			}
		}
		say("\n装好了。已经开着的 agent 要重开一次才会读到新的 MCP 配置。")
		say("在对话里说“用 tick 记录进度”或直接 @tick 即可。")
	}
	return 0
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// 目标正在运行时 Windows 不让覆盖，但允许改名：先把旧的挪开
	if _, err := os.Stat(dst); err == nil {
		_ = os.Remove(dst + ".old")
		if err := os.Rename(dst, dst+".old"); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

func backup(file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return os.WriteFile(fmt.Sprintf("%s.bak-tick-%s", file, time.Now().Format("20060102-150405")), b, 0o644)
}

// mergeJSONMCP：Kiro、Cursor、Gemini 都是 {"mcpServers": {...}}，只动 tick 这一项，别的原样保留
func mergeJSONMCP(file, exe string, dry, remove bool) (string, error) {
	doc := map[string]any{}
	b, err := os.ReadFile(file)
	exists := err == nil
	if exists && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &doc); err != nil {
			return "", fmt.Errorf("%s 不是标准 JSON（可能有注释），没敢改。请手动加：\"tick\": {\"command\": %q, \"args\": [\"mcp\"]}", file, exe)
		}
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	_, had := servers["tick"]
	switch {
	case remove && !had:
		return "· MCP 里本来就没有 tick", nil
	case remove && dry:
		return "· 从 " + file + " 删掉 tick", nil
	case !remove && had:
		return "✓ MCP 已有 tick（" + file + "），没改", nil
	case !remove && dry:
		return "· 在 " + file + " 加上 tick（stdio：tick.exe mcp）", nil
	}
	if remove {
		delete(servers, "tick")
	} else {
		servers["tick"] = map[string]any{"command": exe, "args": []string{"mcp"}}
	}
	doc["mcpServers"] = servers
	if exists {
		if err := backup(file); err != nil {
			return "", fmt.Errorf("备份 %s 失败，没改：%v", file, err)
		}
	}
	out, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(file, append(out, '\n'), 0o644); err != nil {
		return "", err
	}
	if remove {
		return "✓ 已从 " + file + " 删掉 tick", nil
	}
	return "✓ MCP：" + file, nil
}

// codexMCP：~/.codex/config.toml 追加一段 [mcp_servers.tick]；已有就不动
func codexMCP(file, exe string, dry, remove bool) (string, error) {
	b, _ := os.ReadFile(file)
	text := string(b)
	const head = "[mcp_servers.tick]"
	has := strings.Contains(text, head)
	switch {
	case remove && !has:
		return "· config.toml 里本来就没有 tick", nil
	case !remove && has:
		return "✓ MCP 已有 tick（" + file + "），没改", nil
	case dry && remove:
		return "· 从 " + file + " 删掉 [mcp_servers.tick]", nil
	case dry:
		return "· 在 " + file + " 追加 [mcp_servers.tick]", nil
	}
	if len(b) > 0 {
		if err := backup(file); err != nil {
			return "", fmt.Errorf("备份失败，没改：%v", err)
		}
	}
	if remove {
		// 删掉从 [mcp_servers.tick] 到下一个表头之间的内容
		i := strings.Index(text, head)
		j := strings.Index(text[i+len(head):], "\n[")
		end := len(text)
		if j >= 0 {
			end = i + len(head) + j + 1
		}
		text = strings.TrimRight(text[:i], "\n") + "\n" + text[end:]
	} else {
		exeTOML, _ := json.Marshal(exe) // JSON 字符串转义和 TOML 基本字符串兼容
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += fmt.Sprintf("\n%s\ncommand = %s\nargs = [\"mcp\"]\n", head, exeTOML)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		return "", err
	}
	if remove {
		return "✓ 已从 config.toml 删掉 tick", nil
	}
	return "✓ MCP：" + file, nil
}

// claudeMCP：Claude Code 的配置文件很大，交给它自己的命令行改（用户级）
func claudeMCP(exe string, dry, remove bool) (string, error) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return "· 没找到 claude 命令行，MCP 请手动加：claude mcp add --scope user tick -- \"" + exe + "\" mcp", nil
	}
	has := exec.Command(claude, "mcp", "get", "tick").Run() == nil
	switch {
	case remove && !has:
		return "· Claude Code 里本来就没有 tick", nil
	case !remove && has:
		return "✓ MCP 已有 tick（claude mcp get tick），没改", nil
	case dry && remove:
		return "· claude mcp remove --scope user tick", nil
	case dry:
		return "· claude mcp add --scope user tick -- tick.exe mcp", nil
	}
	cmd := exec.Command(claude, "mcp", "add", "--scope", "user", "tick", "--", exe, "mcp")
	if remove {
		cmd = exec.Command(claude, "mcp", "remove", "--scope", "user", "tick")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("claude mcp 执行失败：%v %s", err, strings.TrimSpace(string(out)))
	}
	if remove {
		return "✓ 已从 Claude Code 删掉 tick", nil
	}
	return "✓ MCP：claude mcp（用户级）", nil
}
