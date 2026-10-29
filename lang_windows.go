//go:build windows

package main

import "golang.org/x/sys/windows"

var pGetUserDefaultUILanguage = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")

// systemLang Windows 显示语言是中文（主语言 ID 0x04，简繁都算）就用中文，否则英文
func systemLang() string {
	id, _, _ := pGetUserDefaultUILanguage.Call()
	if id == 0 {
		return envLang()
	}
	if id&0x3ff == 0x04 {
		return "zh"
	}
	return "en"
}
