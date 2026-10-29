package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLangPref(t *testing.T) {
	dir := t.TempDir()
	if got := langPref(dir); got != "" {
		t.Fatalf("没有设置文件时应该跟系统，得到 %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "desktop.json"), []byte(`{"Pinned":true,"Lang":"en"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := langPref(dir); got != "en" {
		t.Fatalf("应读到 en，得到 %q", got)
	}
	defer setLang("zh")
	setLang("en")
	if L("中", "en") != "en" || curLang() != "en" {
		t.Fatal("固定英文后 L 应返回英文")
	}
	setLang("zh")
	if L("中", "en") != "中" || curLang() != "zh" {
		t.Fatal("固定中文后 L 应返回中文")
	}
	setLang("") // 跟系统：只要求结果是两者之一
	if l := curLang(); l != "zh" && l != "en" {
		t.Fatalf("跟系统时语言应为 zh 或 en，得到 %q", l)
	}
}
