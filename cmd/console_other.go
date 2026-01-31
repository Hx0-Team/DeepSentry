//go:build !windows
// +build !windows

package main

// Mac/Linux 原生支持 ANSI 颜色，不需要做任何事
func enableWindowsANSI() {
	// 空函数，仅为了兼容接口
}
