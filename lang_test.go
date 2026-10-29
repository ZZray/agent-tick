package main

import (
	"encoding/json"
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

// 推送给页面的会话快照只带最近 pushEvents 条动态；GET 取到的还是完整的
func TestPushTrimsEvents(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Flush() })
	str := func(v string) *string { return &v }
	s, _, err := st.Start(StartInput{Agent: str("kiro"), Title: str("动态裁剪")})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pushEvents+20; i++ {
		if _, err := st.Log(s.ID, str("一条动态")); err != nil {
			t.Fatal(err)
		}
	}
	ch, cancel := st.Subscribe()
	defer cancel()
	if _, err := st.Log(s.ID, str("最后一条")); err != nil {
		t.Fatal(err)
	}
	f := <-ch
	var got Session
	if err := json.Unmarshal(f.Data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != pushEvents || got.Events[len(got.Events)-1].Text != "最后一条" {
		t.Fatalf("推送应只带最近 %d 条且以最新一条结尾，得到 %d 条", pushEvents, len(got.Events))
	}
	full, _ := st.Session(s.ID)
	if len(full.Events) <= pushEvents {
		t.Fatalf("完整会话不应被裁剪，得到 %d 条", len(full.Events))
	}
}
