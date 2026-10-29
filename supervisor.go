package main

// 崩溃自动重启：桌面模式下 tick.exe 先当“守护进程”，再拉起一个干活的子进程（TICK_WORKER=1）。
// 子进程正常退出（托盘“退出”，退出码 0）守护也退出；崩溃（panic、被杀、界面线程卡死自行退出）就隔一会儿重启，
// 退避 1s → 2s → 5s → 10s → 30s；10 分钟内崩 5 次就不再硬撑，弹窗告诉用户去看 crash.log。
// 子进程的 stderr（Go 的 panic 堆栈在这里）追加写到数据目录的 crash.log。
// 更新：子进程换好新文件后以退出码 75 退出，守护用同一路径（已是新版本）启动新的守护，自己退出。

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	exitRestart = 75 // 子进程：已换好新版本，请用新文件重启
	exitHung    = 3  // 子进程：界面线程卡死，自己退出等守护重启
	exitFatal   = 4  // 子进程：配置类错误（端口被占、数据文件打不开），已经弹窗告诉用户，重启也没用
)

// exitCode 子进程退出时用的退出码（更新重启时设成 exitRestart）
var exitCode int

func isWorker() bool { return os.Getenv("TICK_WORKER") == "1" }

// supervise 守护循环；args 原样传给子进程
func supervise(dataDir string, args []string) {
	exe, err := os.Executable()
	if err != nil {
		fatal("找不到程序路径：" + err.Error())
	}
	exe = filepath.Clean(exe) // 先记下来：更新时这个路径上会换成新文件，守护自己的映像被改名成 .old
	crashes := []time.Time{}
	backoff := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	restarting := os.Getenv("TICK_RESTART") == "1"
	for {
		start := time.Now()
		code, err := runWorker(exe, dataDir, args, restarting)
		switch {
		case err != nil:
			fatal("Tick 起不来：" + err.Error())
		case code == 0, code == exitFatal:
			return
		case code == exitRestart:
			// 用新文件起一个新的守护；端口要等旧子进程完全退出，新进程会等（TICK_RESTART=1）
			cmd := exec.Command(exe, args...)
			cmd.Env = append(os.Environ(), "TICK_RESTART=1", "TICK_WORKER=")
			if err := cmd.Start(); err != nil {
				log.Printf("重启到新版本失败：%v，继续用当前版本", err)
				restarting = true
				continue
			}
			return
		}
		// 崩了。跑满 10 分钟的不算连续崩溃
		now := time.Now()
		if now.Sub(start) > 10*time.Minute {
			crashes = crashes[:0]
		}
		crashes = append(crashes, now)
		for len(crashes) > 0 && now.Sub(crashes[0]) > 10*time.Minute {
			crashes = crashes[1:]
		}
		if len(crashes) >= 5 {
			fatal(fmt.Sprintf(L("Tick 10 分钟内崩溃了 %d 次，先不自动重启了。\n崩溃记录：%s\n可以把它发给开发者，然后重新打开 Tick。",
				"Tick crashed %d times in 10 minutes, so it will not restart automatically.\nCrash log: %s\nYou can send it to the developer, then start Tick again."), len(crashes), filepath.Join(dataDir, "crash.log")))
		}
		wait := backoff[min(len(crashes)-1, len(backoff)-1)]
		log.Printf("Tick 子进程异常退出（退出码 %d，运行了 %s），%s 后重启", code, now.Sub(start).Round(time.Second), wait)
		time.Sleep(wait)
		restarting = true
	}
}

// runWorker 起一个子进程并等它结束，返回退出码
func runWorker(exe, dataDir string, args []string, restarting bool) (int, error) {
	crash, err := os.OpenFile(filepath.Join(dataDir, "crash.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer crash.Close()
	trimCrashLog(crash)
	fmt.Fprintf(crash, "\n==== %s 启动 Tick %s ====\n", time.Now().Format("2006-01-02 15:04:05"), version)
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "TICK_WORKER=1")
	if restarting {
		cmd.Env = append(cmd.Env, "TICK_RESTART=1")
	}
	cmd.Stderr = crash
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

// trimCrashLog crash.log 超过 1MB 就只留后一半
func trimCrashLog(f *os.File) {
	st, err := f.Stat()
	if err != nil || st.Size() < 1<<20 {
		return
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return
	}
	_ = os.WriteFile(f.Name(), b[len(b)/2:], 0o644)
}
