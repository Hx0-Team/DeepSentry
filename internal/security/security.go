package security

import (
	"ai-edr/internal/executor"
	"crypto/md5"
	"fmt"
	"strings"
	"sync"
)

// approvedCache 用于记录用户已授权的高危命令哈希
// 作用：一旦用户批准某条命令，本次运行期间不再重复询问
var (
	approvedCache = make(map[string]bool)
	cacheMutex    sync.RWMutex
)

// RecordApproval 记录用户已批准的命令
func RecordApproval(cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(cmd)))

	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	approvedCache[hash] = true
}

// isApproved 检查命令是否已被批准过
func isApproved(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(cmd)))

	cacheMutex.RLock()
	defer cacheMutex.RUnlock()
	return approvedCache[hash]
}

// CheckRisk 评估命令的风险等级
// 返回值: (riskLevel: "high"|"low", reason: string)
func CheckRisk(cmd string) (string, string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "low", "空命令"
	}

	// 0. [Session Cache] 检查是否是用户已批准过的命令
	if isApproved(cmd) {
		return "low", "用户已授权 (Session)"
	}

	// 1. 预处理：移除 local_run 等前缀并清洗
	analyzeCmd := cmd
	if strings.HasPrefix(cmd, "local_run ") {
		analyzeCmd = strings.TrimPrefix(cmd, "local_run ")
	}
	analyzeCmd = cleanShellWrapper(analyzeCmd)

	// 2. 全局高危特征检测
	// 检测重定向 (>)，防止文件覆盖风险
	if strings.Contains(analyzeCmd, ">") {
		return "high", "检测到文件重定向 (>)"
	}

	// 3. 复合命令拆分逻辑
	// 将 &&, ;, || 统一替换为分隔符并拆分，逐个检查
	normalizedCmd := analyzeCmd
	normalizedCmd = strings.ReplaceAll(normalizedCmd, "&&", "::SPLIT::")
	normalizedCmd = strings.ReplaceAll(normalizedCmd, ";", "::SPLIT::")
	normalizedCmd = strings.ReplaceAll(normalizedCmd, "||", "::SPLIT::")

	subCmds := strings.Split(normalizedCmd, "::SPLIT::")

	// 4. 逐个分析子命令
	for _, sub := range subCmds {
		// 只要有一个子命令是高危，整体就是高危
		risk, reason := checkSingleCommand(sub)
		if risk == "high" {
			return "high", reason
		}
	}

	// 所有子命令都通过检查
	return "low", "安全操作"
}

// cleanShellWrapper 清洗 Shell 包装器和引号
func cleanShellWrapper(cmd string) string {
	cmd = strings.TrimSpace(cmd)

	// 移除常见 Shell 前缀 (不区分大小写的简单处理)
	prefixes := []string{"/bin/sh -c", "sh -c", "/bin/bash -c", "bash -c", "cmd /c", "powershell -Command", "powershell -c"}
	for _, p := range prefixes {
		if len(cmd) > len(p) && strings.EqualFold(cmd[:len(p)], p) {
			cmd = cmd[len(p):]
			cmd = strings.TrimSpace(cmd)
			break
		}
	}

	// 移除首尾的引号 (单引号或双引号)
	if len(cmd) >= 2 {
		first := cmd[0]
		last := cmd[len(cmd)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			cmd = cmd[1 : len(cmd)-1]
		}
	}

	return strings.TrimSpace(cmd)
}

// checkSingleCommand 单个命令判定逻辑
func checkSingleCommand(subCmd string) (string, string) {
	subCmd = strings.TrimSpace(subCmd)
	if subCmd == "" {
		return "low", ""
	}

	parts := strings.Fields(subCmd)
	if len(parts) == 0 {
		return "low", ""
	}

	// 获取动词并转小写
	verb := strings.ToLower(parts[0])
	// 二次清洗：防止动词本身带引号 (如 "cd")
	verb = strings.Trim(verb, "\"'")

	// --- 白名单 (Low Risk) ---
	lowRiskVerbs := map[string]bool{
		// 浏览与查看
		"ls": true, "dir": true, "pwd": true, "cd": true,
		"cat": true, "echo": true, "head": true, "tail": true,
		"more": true, "less": true, "tree": true,
		"find": true, "grep": true, "findstr": true,
		"stat": true, "file": true, "where": true, "which": true,

		// 系统/网络信息
		"whoami": true, "id": true, "hostname": true, "uname": true,
		"uptime": true, "date": true, "w": true,
		"ps": true, "top": true, "tasklist": true, "free": true, "df": true, "du": true,
		"ipconfig": true, "ifconfig": true, "ip": true, "netstat": true, "ss": true,
		"ping": true, "arp": true, "route": true, "nslookup": true, "dig": true,
		"wmic": true, "ver": true,

		// 文件操作 (非破坏性)
		"mkdir": true, "touch": true, "type": true,

		// 🟢 [新增] PowerShell 常用安全动词
		// 注意：已移除重复的 "ls"
		"get-childitem": true, "gci": true,
		"get-content": true, "gc": true,
		"get-location": true, "gl": true,
		"get-process": true, "gps": true,
		"get-service": true, "gsv": true,
		"get-date": true, "get-host": true,
		"write-host": true, "write-output": true,
		"select-object": true, "where-object": true, "foreach-object": true,
	}

	if lowRiskVerbs[verb] {
		return "low", "安全操作"
	}

	// --- 黑名单 (High Risk) ---
	highRiskVerbs := map[string]bool{
		// 破坏性操作
		"rm": true, "del": true, "erase": true, "rmdir": true,
		"mv": true, "move": true, "cp": true, "copy": true,
		"mkfs": true, "format": true, "fdisk": true, "dd": true,
		"shred": true, "wipe": true,

		// 系统控制与权限
		"reboot": true, "shutdown": true, "halt": true, "poweroff": true, "init": true,
		"systemctl": true, "service": true, "sc": true, "reg": true,
		"chmod": true, "chown": true, "chgrp": true, "attrib": true,
		"useradd": true, "usermod": true, "userdel": true, "passwd": true,
		"sudo": true, "su": true,

		// 进程与网络传输
		"kill": true, "pkill": true, "killall": true, "taskkill": true,
		"wget": true, "curl": true, "nc": true, "ncat": true,

		// PowerShell 敏感操作
		"invoke-expression": true, "iex": true,
		"start-process": true,
	}

	if highRiskVerbs[verb] {
		return "high", fmt.Sprintf("敏感指令: %s", verb)
	}

	// --- 默认策略 ---
	return "high", fmt.Sprintf("未知指令(%s)，需人工确认", verb)
}

// SafeExecV3 执行命令的安全封装
func SafeExecV3(cmd string) (string, error) {
	if executor.Current == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	return executor.Current.Run(cmd)
}
