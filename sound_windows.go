//go:build windows

package main

// 通知提示音：默认不响。可选内置的几种短音，或者把自己的 .wav 放进 数据目录\sounds\。
// 系统通知本身一律静音（toast.Silent），声音只从这里出，不会响两遍。

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed sounds/*.wav
var soundFS embed.FS

type soundOpt struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// 内置的几种，按“清脆 → 轻铃 → 柔和”排；ID 写进设置里，改名不影响已选
var builtinSounds = []soundOpt{
	{"crisp", "清脆"}, {"crisp-single", "清脆 · 单声"}, {"crisp-double", "清脆 · 两下"},
	{"bell", "轻铃"}, {"bell-triad", "轻铃 · 三音"},
	{"soft", "柔和"}, {"soft-minor", "柔和 · 小三度"}, {"soft-down", "柔和 · 下行"},
}

var (
	pPlaySound = windows.NewLazySystemDLL("winmm.dll").NewProc("PlaySoundW")
	soundMu    sync.Mutex
	playing    []byte // 异步播放期间这块内存必须一直有效（Go 的 GC 不移动对象，留住引用即可）
)

const sndAsync, sndNoDefault, sndMemory = 0x1, 0x2, 0x4

func (f *floatWin) soundDir() string { return filepath.Join(filepath.Dir(f.file), "sounds") }

// sounds 内置的 + 用户文件夹里的 .wav（ID 以 file: 开头）
var soundNamesEN = map[string]string{
	"crisp": "Crisp", "crisp-single": "Crisp · single", "crisp-double": "Crisp · double",
	"bell": "Bell", "bell-triad": "Bell · triad", "soft": "Soft", "soft-minor": "Soft · minor third", "soft-down": "Soft · falling",
}

func (f *floatWin) sounds() []soundOpt {
	list := slices.Clone(builtinSounds)
	if langEN.Load() {
		for i := range list {
			list[i].Name = soundNamesEN[list[i].ID]
		}
	}
	if ents, err := os.ReadDir(f.soundDir()); err == nil {
		for _, e := range ents {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".wav") {
				list = append(list, soundOpt{"file:" + e.Name(), strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))})
			}
		}
	}
	return list
}

func (f *floatWin) soundID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set.Sound
}

// soundBytes 读出某个声音；找不到（文件被删了）返回 nil，当作静音
func (f *floatWin) soundBytes(id string) []byte {
	if name, ok := strings.CutPrefix(id, "file:"); ok {
		if filepath.Base(name) != name { // 只认文件夹里的文件名，不接受路径
			return nil
		}
		b, _ := os.ReadFile(filepath.Join(f.soundDir(), name))
		return b
	}
	b, _ := soundFS.ReadFile("sounds/" + id + ".wav")
	return b
}

// play 异步播放一段 wav；新的会打断还没放完的旧的
func play(b []byte) {
	if len(b) == 0 {
		return
	}
	soundMu.Lock()
	defer soundMu.Unlock()
	playing = b
	if r, _, err := pPlaySound.Call(uintptr(unsafe.Pointer(&playing[0])), 0, sndMemory|sndAsync|sndNoDefault); r == 0 {
		log.Printf("提示音没放出来（不是有效的 wav，或者没有音频设备）：%v", err)
	}
}

// setSound 换提示音并试听一下；"" 表示不响
func (f *floatWin) setSound(id string) {
	if id != "" && !slices.ContainsFunc(f.sounds(), func(o soundOpt) bool { return o.ID == id }) {
		return
	}
	f.mu.Lock()
	f.set.Sound = id
	f.mu.Unlock()
	f.saveSettings()
	if id != "" {
		play(f.soundBytes(id))
	}
	f.pushHostState()
	f.changed()
}

// openSoundDir 在资源管理器里打开自定义声音文件夹，没有就先建一个
func (f *floatWin) openSoundDir() {
	_ = os.MkdirAll(f.soundDir(), 0o755)
	verb, _ := windows.UTF16PtrFromString("open")
	dir, _ := windows.UTF16PtrFromString(f.soundDir())
	_ = windows.ShellExecute(0, verb, dir, nil, nil, windows.SW_SHOWNORMAL)
}

func (f *floatWin) playNotice() {
	if id := f.soundID(); id != "" {
		play(f.soundBytes(id))
	}
}

// soundState 给页面的 JSON 片段：当前选的和可选列表
func (f *floatWin) soundState() string {
	b, _ := json.Marshal(f.sounds())
	id, _ := json.Marshal(f.soundID())
	return fmt.Sprintf("sound:%s,sounds:%s", id, b)
}
