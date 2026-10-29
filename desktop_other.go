//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// 非 Windows 只提供服务和网页（浏览器里用置顶小窗）
const desktopSupported = false

func runDesktop(_ *Server, serve func()) { serve() }
func showExisting(port int) error {
	fmt.Printf("Tick 已在运行：http://127.0.0.1:%d\n", port)
	return nil
}
func hideConsole(*exec.Cmd)                             {}
func execCommand(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }
func attachConsole()                                    {}
func fatal(msg string)                                  { fmt.Fprintln(os.Stderr, msg); os.Exit(1) }
