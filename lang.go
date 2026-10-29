package main

// 界面语言：中文或英文。默认跟系统显示语言（Windows 取用户界面语言，其他系统看 LANG / LC_ALL），
// 托盘菜单里可以改成固定中文或英文。只管给用户看的文字（托盘、弹窗、页面）；
// 给 agent 看的接口错误、命令行输出和协议文档不翻译。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

var langEN atomic.Bool

// L 按当前语言二选一
func L(zh, en string) string {
	if langEN.Load() {
		return en
	}
	return zh
}

func curLang() string { return L("zh", "en") }

// setLang 设置界面语言：pref 为 "zh" / "en" 时固定，其他值（包括空）跟系统
func setLang(pref string) {
	l := pref
	if l != "zh" && l != "en" {
		l = systemLang()
	}
	langEN.Store(l == "en")
}

// langPref 读桌面设置里用户选的语言（desktop.json 的 Lang）；没有或读不出来就是空，跟系统
func langPref(dataDir string) string {
	var s struct{ Lang string }
	if b, err := os.ReadFile(filepath.Join(dataDir, "desktop.json")); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s.Lang
}

// envLang 从 LANG / LC_ALL / LC_MESSAGES 判断（非 Windows，或 Windows 上取不到时）
func envLang() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := strings.ToLower(os.Getenv(k)); v != "" {
			if strings.HasPrefix(v, "zh") {
				return "zh"
			}
			return "en"
		}
	}
	return "en"
}
