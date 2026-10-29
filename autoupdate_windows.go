//go:build windows

package main

// 桌面端的自动更新：启动 1 分钟后检查一次，之后每 12 小时一次。
// 有新版就下载校验换好文件，等到没有会话在跑、悬浮窗也没被用的时候再重启，不打断正在进行的工作。

import (
	"log"
	"time"

	"fyne.io/systray"
)

type updater struct {
	f       *floatWin
	srv     *Server
	pending string // 已换好文件、等着重启的版本
	menu    *systray.MenuItem
}

func (u *updater) busy() bool {
	for _, s := range u.srv.store.Sessions(false) {
		if s.Status == "running" || s.Status == "planning" {
			return true
		}
	}
	return false
}

// check 检查并下载。manual 为 true 时（托盘点“检查更新”）把结果告诉用户
func (u *updater) check(manual bool) {
	tag, r, err := checkUpdate()
	say := func(text, kind string) {
		agent, k := "tick", kind
		if _, err := u.srv.store.Notify(NotifyInput{Agent: &agent, Kind: &k, Text: &text}); err != nil {
			log.Printf("更新通知没发出去：%v", err)
		}
	}
	switch {
	case err != nil:
		log.Printf("检查更新：%v", err)
		if manual {
			say("检查更新没成功："+err.Error(), "warning")
		}
	case tag == "":
		if manual {
			say("已经是最新版 "+version, "success")
		}
	default:
		if err := applyUpdate(r); err != nil {
			log.Printf("更新到 %s 失败：%v", tag, err)
			say("下载新版本 "+tag+" 没成功："+err.Error(), "warning")
			return
		}
		u.pending = tag
		u.menu.SetTitle("重启到新版本 " + tag)
		log.Printf("已下载新版本 %s，等空闲时重启", tag)
		say("新版本 "+tag+" 已下载，没有会话在跑时会自动重启；也可以在托盘里点“重启到新版本”", "info")
	}
}

// restartWhenIdle：没有会话在跑就马上重启，否则每分钟看一次，最多等 6 小时
func (u *updater) restartWhenIdle(force bool) {
	for i := 0; !force && u.busy() && i < 360; i++ {
		time.Sleep(time.Minute)
	}
	if err := relaunch(); err != nil {
		log.Printf("重启到新版本失败：%v", err)
		return
	}
	log.Printf("重启到 %s", u.pending)
	u.f.dispatch(func() {
		u.f.saveSettings()
		u.f.w.Terminate()
	})
	systray.Quit()
}

func (u *updater) loop() {
	time.Sleep(time.Minute)
	for {
		u.f.mu.Lock()
		off := u.f.set.NoAutoUpdate
		u.f.mu.Unlock()
		if !off && u.pending == "" && updateSource() != "" {
			u.check(false)
			if u.pending != "" {
				u.restartWhenIdle(false)
				return
			}
		}
		time.Sleep(12 * time.Hour)
	}
}
