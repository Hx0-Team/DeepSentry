//go:build windows
// +build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableWindowsANSI 负责 Windows 平台的控制台初始化：
// 1. 强制开启 UTF-8 编码 (解决中文乱码)
// 2. 强制开启虚拟终端处理 (解决颜色乱码)
func enableWindowsANSI() {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")

	// --- 1. 设置控制台输出代码页为 UTF-8 (Code Page 65001) ---
	setConsoleOutputCP := kernel32.NewProc("SetConsoleOutputCP")
	// 65001 是 UTF-8 的代码页标识符
	setConsoleOutputCP.Call(uintptr(65001))

	// --- 2. 开启 ANSI 颜色支持 (Virtual Terminal Processing) ---
	stdout := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(stdout, &mode); err == nil {
		mode |= windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
		windows.SetConsoleMode(stdout, mode)
	}
}
