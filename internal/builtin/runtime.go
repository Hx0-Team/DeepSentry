package builtin

import (
	"ai-edr/internal/executor"
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// Runtime 工具运行时上下文
type Runtime struct {
	IsWindows bool
	IsRemote  bool
	Exec      executor.Executor
	Context   context.Context
}

// NewRuntime 从系统指纹构造运行时
func NewRuntime(osHint string, isRemote bool) Runtime {
	return Runtime{
		IsWindows: strings.Contains(strings.ToLower(osHint), "windows"),
		IsRemote:  isRemote,
		Exec:      executor.Current,
	}
}

func (rt Runtime) tag() string {
	if rt.IsRemote {
		return "[Go内置·远程/proc]"
	}
	return "[Go内置·本地]"
}

func (rt Runtime) readTarget(path string) ([]byte, error) {
	ex := rt.executor()
	if ex == nil {
		return nil, fmt.Errorf("执行器未初始化")
	}
	return ex.ReadTargetFile(path)
}

func (rt Runtime) listTarget(path string) ([]string, error) {
	ex := rt.executor()
	if ex == nil {
		return nil, fmt.Errorf("执行器未初始化")
	}
	return ex.ListTargetDir(path)
}

func (rt Runtime) isTargetDir(path string) bool {
	ex := rt.executor()
	if ex == nil {
		return false
	}
	if !ex.IsRemote() {
		st, err := os.Stat(path)
		return err == nil && st.IsDir()
	}
	out, err := ex.Run("test -d " + shellQuote(path) + " && echo __DIR__ || echo __FILE__")
	if err != nil {
		return false
	}
	return strings.Contains(out, "__DIR__")
}

func (rt Runtime) readTargetLink(path string) (string, error) {
	ex := rt.executor()
	if ex == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	if !ex.IsRemote() {
		return os.Readlink(path)
	}
	out, err := ex.Run("readlink " + shellQuote(path))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func WithExecutor(rt Runtime, ex executor.Executor) Runtime {
	rt.Exec = ex
	rt.IsRemote = ex != nil && ex.IsRemote()
	return rt
}

func (rt Runtime) executor() executor.Executor {
	if rt.Exec != nil {
		return rt.Exec
	}
	return executor.Current
}

func (rt Runtime) context() context.Context {
	if rt.Context != nil {
		return rt.Context
	}
	return context.Background()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return s[:end] + "\n...(输出已截断)..."
}
