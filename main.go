// Tick：本机的 agent 任务进度与通知。一个 exe：HTTP API + MCP + 内嵌网页，Windows 上再带托盘和置顶悬浮窗。
//
//	tick                 启动服务、托盘和悬浮窗
//	tick -headless       只启动服务（浏览器访问，或给别的机器做后台）
//	tick mcp             stdio MCP 桥，给只支持 stdio 的客户端用；服务没开会自动拉起
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func defaultDataDir() string {
	if d := os.Getenv("TICK_DATA_DIR"); d != "" {
		return d
	}
	if d, err := os.UserConfigDir(); err == nil { // Windows 上是 %APPDATA%
		return filepath.Join(d, "Tick")
	}
	return ".tick"
}

func defaultPort() int {
	if p, err := strconv.Atoi(os.Getenv("TICK_PORT")); err == nil && p > 0 {
		return p
	}
	return 7357
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(runBridge(defaultPort()))
	}
	if len(os.Args) > 1 && (os.Args[1] == "install" || os.Args[1] == "uninstall") {
		attachConsole()
		os.Exit(runInstall(os.Args[1:]))
	}
	if len(os.Args) > 1 && (os.Args[1] == "update" || os.Args[1] == "version") {
		attachConsole()
		os.Exit(runUpdateCLI(os.Args[1]))
	}
	if len(os.Args) > 1 && cliCommands[os.Args[1]] {
		attachConsole() // GUI 程序从终端调用时把输出接回终端
		os.Exit(runCLI(defaultPort(), os.Args[1:]))
	}
	port := flag.Int("port", defaultPort(), "监听端口（只绑定 127.0.0.1）")
	dataDir := flag.String("data", defaultDataDir(), "数据目录")
	headless := flag.Bool("headless", false, "只启动服务，不显示托盘和悬浮窗")
	flag.Parse()

	logFile := setupLog(*dataDir)
	defer logFile.Close()

	// 桌面模式：先当守护进程，真正干活的是子进程，崩了能自动重启（见 supervisor.go）。
	// 已经有一个 Tick 在跑就只把它叫出来
	if !*headless && desktopSupported && !isWorker() && os.Getenv("TICK_NO_SUPERVISOR") != "1" {
		if os.Getenv("TICK_RESTART") != "1" && pingTick(*port) {
			_ = showExisting(*port)
			return
		}
		supervise(*dataDir, os.Args[1:])
		return
	}

	cleanupOld()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	// 更新后重启：旧进程还在退出，端口要等一会儿才空出来
	for i := 0; err != nil && os.Getenv("TICK_RESTART") == "1" && i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	}
	if err != nil {
		// 已经有一个 Tick 在跑：把它的悬浮窗叫出来就退出
		if pingTick(*port) {
			_ = showExisting(*port)
			return
		}
		fatal(fmt.Sprintf("端口 %d 被别的程序占用了。用 -port 换一个端口，或设置 TICK_PORT。", *port))
	}
	store, err := OpenStore(*dataDir)
	if err != nil {
		fatal("数据文件打不开：" + err.Error())
	}
	srv := &Server{store: store, port: *port, desktop: !*headless && desktopSupported, mcp: newMCPSessions()}
	serve := func() {
		httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("HTTP 服务退出：%v", err)
			}
		}()
		log.Printf("Tick %s 已启动：http://127.0.0.1:%d  数据：%s", version, *port, *dataDir)
	}

	if srv.desktop {
		runDesktop(srv, serve) // 桌面端挂好回调后才开始接请求；阻塞到用户从托盘退出
		logFile.Close()
		os.Exit(exitCode) // 0 = 正常退出；exitRestart = 已换好新版本，守护进程用新文件重启
	}
	serve()
	fmt.Printf("Tick %s 已启动：http://127.0.0.1:%d\nMCP：http://127.0.0.1:%d/mcp\n数据：%s\n", version, *port, *port, *dataDir)
	select {}
}

// 日志同时写到数据目录，GUI 模式没有控制台时也能查
func setupLog(dir string) *os.File {
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "tick.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return os.Stderr
	}
	log.SetOutput(f)
	return f
}

func pingTick(port int) bool {
	c := http.Client{Timeout: 1500 * time.Millisecond}
	r, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/health", port))
	if err != nil {
		return false
	}
	r.Body.Close()
	return r.StatusCode == 200
}
