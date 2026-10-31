//go:build windows

package main

import (
	"context"
	"slices"
)

// settings runs reads and writes on the float owner thread and reports completion,
// rather than returning before a queued show/hide or guessing with a sleep.
func (t *tray) settings(ctx context.Context, in *desktopSettingsInput) (any, func(), error) {
	type result struct {
		state   trayState
		restart bool
		err     error
	}
	done := make(chan result, 1)
	unconfirmed := &apiError{503, "桌面设置请求未确认完成，请刷新后核对实际状态"}
	t.f.dispatch(func() {
		if err := ctx.Err(); err != nil {
			done <- result{err: unconfirmed}
			return
		}
		var restart bool
		var err error
		if in != nil {
			on, val := false, ""
			if in.On != nil {
				on = *in.On
			}
			if in.Val != nil {
				val = *in.Val
			}
			restart, err = t.applySetting(in.Name, on, val)
		}
		if err != nil {
			done <- result{err: err}
			return
		}
		if in != nil && t.refresh != nil {
			t.refresh()
		}
		state := t.state()
		state.Restart = restart
		if restart {
			state.EffectiveLang = state.Lang
			if state.EffectiveLang == "" {
				state.EffectiveLang = systemLang()
			}
		}
		done <- result{state: state, restart: restart}
	})
	select {
	case r := <-done:
		if r.err != nil {
			return nil, nil, r.err
		}
		var afterReply func()
		if r.restart {
			afterReply = func() { go t.up.restartWhenIdle(true) }
		}
		return r.state, afterReply, nil
	case <-ctx.Done():
		return nil, nil, unconfirmed
	}
}

// applySetting is shared by the settings API and tray actions. Language changes
// return a restart request so the HTTP response can be flushed before restarting.
func (t *tray) applySetting(name string, on bool, val string) (bool, error) {
	f := t.f
	switch name {
	case "float":
		if on {
			f.summon()
		} else {
			f.hide()
		}
		return false, f.saveSettings()
	case "dnd":
		return false, f.setDnd(on)
	case "autostart":
		return false, setAutostart(on)
	case "sound":
		return false, f.setSound(val)
	case "toastlen":
		if !slices.Contains(toastLens, val) {
			return false, bad("不支持的提醒时长：%s", val)
		}
		f.mu.Lock()
		f.set.ToastLen = val
		f.mu.Unlock()
		err := f.saveSettings()
		f.pushHostState()
		return false, err
	case "theme":
		if !slices.ContainsFunc(themes, func(t colorTheme) bool { return t.ID == val }) {
			return false, bad("不支持的主题色：%s", val)
		}
		f.mu.Lock()
		f.set.Theme = val
		f.mu.Unlock()
		err := f.saveSettings()
		f.pushHostState()
		return false, err
	case "lang":
		if val != "" && val != "zh" && val != "en" {
			return false, bad("不支持的界面语言：%s", val)
		}
		f.mu.Lock()
		changed := f.set.Lang != val
		f.set.Lang = val
		f.mu.Unlock()
		if err := f.saveSettings(); err != nil {
			return false, err
		}
		effective := val
		if effective == "" {
			effective = systemLang()
		}
		return changed || effective != curLang(), nil
	default:
		return false, bad("不支持的设置动作：%s", name)
	}
}
