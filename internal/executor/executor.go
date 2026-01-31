package executor

import (
	"ai-edr/internal/config"
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// Executor 接口定义了执行器的标准行为
type Executor interface {
	Run(cmd string) (string, error)
	IsRemote() bool
	Close()
}

// Current 全局变量，存储当前活动的执行器实例
var Current Executor

// Init 初始化执行器
func Init(cfg config.Config) error {
	if cfg.SSHHost != "" {
		e, err := newSSHExecutor(cfg)
		if err != nil {
			return err
		}
		Current = e
		fmt.Printf("🔌 [模式切换] 已连接至远程主机 (SSH): %s@%s\n", cfg.SSHUser, cfg.SSHHost)
	} else {
		Current = &LocalExecutor{}
		fmt.Println("🔌 [模式切换] 本地执行模式")
	}
	return nil
}

// ==========================================
// Local Executor (本地模式)
// ==========================================

type LocalExecutor struct{}

func (l *LocalExecutor) Run(cmdStr string) (string, error) {
	// 1. 清洗 local_run 标记
	if strings.Contains(cmdStr, "local_run ") {
		cmdStr = strings.ReplaceAll(cmdStr, "local_run ", "")
	}
	cmdStr = strings.TrimSpace(cmdStr)

	// 2. 拦截 download/upload
	if strings.HasPrefix(cmdStr, "download ") || strings.HasPrefix(cmdStr, "upload ") {
		parts := strings.Fields(cmdStr)
		if len(parts) != 3 {
			return "", fmt.Errorf("用法错误: transfer <src> <dst>")
		}
		return copyLocalFile(parts[1], parts[2])
	}

	// 3. 执行命令
	var cmd *exec.Cmd
	var out []byte
	var err error

	// 🟢 [核心优化] 智能判断执行引擎
	lowerCmd := strings.ToLower(cmdStr)
	// 判断是否显式调用 PowerShell
	isPowerShell := strings.HasPrefix(lowerCmd, "powershell") || strings.HasPrefix(lowerCmd, "pwsh")

	if runtime.GOOS == "windows" {
		if isPowerShell {
			// === PowerShell 直连模式 ===
			// 提取纯脚本内容，避开 cmd /c 对特殊字符和变量的干扰
			script := cmdStr

			// 简单的去除前缀逻辑
			if strings.HasPrefix(lowerCmd, "powershell") {
				script = script[10:] // len("powershell")
			} else if strings.HasPrefix(lowerCmd, "pwsh") {
				script = script[4:]
			}
			script = strings.TrimSpace(script)

			// 去掉参数前缀
			lowerScript := strings.ToLower(script)
			if strings.HasPrefix(lowerScript, "-command ") {
				script = script[9:]
			} else if strings.HasPrefix(lowerScript, "-c ") {
				script = script[3:]
			}
			script = strings.Trim(script, " \"'") // 去掉包裹脚本的引号

			// 直接调用 powershell.exe
			// -NoProfile: 不加载用户配置，加速
			// -NonInteractive: 非交互模式
			// -Command: 执行脚本
			cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
		} else {
			// === CMD 模式 ===
			// 这种模式下，Windows 默认输出 GBK 编码
			cmd = exec.Command("cmd", "/c", cmdStr)
		}
	} else {
		// Linux/Mac 模式
		cmd = exec.Command("sh", "-c", cmdStr+" 2>&1")
	}

	out, err = cmd.CombinedOutput()

	// 4. [智能转码] Windows GBK -> UTF-8
	// 只有在 Windows 且非 PowerShell 直连的情况下（CMD模式），才极大概率出现 GBK 乱码
	// PowerShell 较新版本通常输出 UTF-8，但也视配置而定。
	// 安全起见，我们尝试探测并转换。
	if runtime.GOOS == "windows" {
		// 尝试将输出视为 GBK 并转换为 UTF-8
		// 如果转换后的内容是有效的 UTF-8 且看起来合理，就使用它
		if utf8Out, transformErr := GbkToUtf8(out); transformErr == nil {
			// 简单的启发式判断：如果转换没报错，且长度变化不大，通常就是对的
			out = utf8Out
		}
	}

	// 5. 结果清洗
	outputStr := string(out)
	// 清洗 Windows 代码页提示噪音
	outputStr = strings.ReplaceAll(outputStr, "Active code page: 65001\r\n", "")
	outputStr = strings.ReplaceAll(outputStr, "Active code page: 65001\n", "")
	outputStr = strings.TrimSpace(outputStr)

	if outputStr == "" && err == nil {
		outputStr = "(执行成功，无输出)"
	}

	return outputStr, err
}

func (l *LocalExecutor) IsRemote() bool { return false }
func (l *LocalExecutor) Close()         {}

func copyLocalFile(src, dst string) (string, error) {
	sourceFile, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("打开源文件失败: %v", err)
	}
	defer sourceFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return "", fmt.Errorf("创建目录失败: %v", err)
	}

	destFile, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("创建目标文件失败: %v", err)
	}
	defer destFile.Close()

	n, err := io.Copy(destFile, sourceFile)
	if err != nil {
		return "", fmt.Errorf("复制失败: %v", err)
	}
	return fmt.Sprintf("✅ 文件传输成功 (Bytes: %d): %s -> %s", n, src, dst), nil
}

// ==========================================
// SSH Executor (远程模式)
// ==========================================

type SSHExecutor struct {
	client     *ssh.Client
	session    *ssh.Session
	sftpClient *sftp.Client
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	mu         sync.Mutex
}

func newSSHExecutor(cfg config.Config) (*SSHExecutor, error) {
	var authMethods []ssh.AuthMethod
	if cfg.SSHKeyPath != "" {
		key, err := os.ReadFile(cfg.SSHKeyPath)
		if err != nil {
			return nil, fmt.Errorf("读取私钥失败: %v", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %v", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	} else {
		authMethods = append(authMethods, ssh.Password(cfg.SSHPassword))
	}

	sshConfig := &ssh.ClientConfig{
		User:            cfg.SSHUser,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", cfg.SSHHost, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("SSH连接失败: %v", err)
	}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("SFTP 初始化失败: %v", err)
	}

	session, err := client.NewSession()
	if err != nil {
		sftpClient.Close()
		client.Close()
		return nil, fmt.Errorf("Session 创建失败: %v", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	session.Stderr = session.Stdout

	if err := session.Start("/bin/bash"); err != nil {
		if err := session.Start("/bin/sh"); err != nil {
			return nil, fmt.Errorf("无法启动远程Shell: %v", err)
		}
	}

	exe := &SSHExecutor{
		client:     client,
		session:    session,
		sftpClient: sftpClient,
		stdin:      stdin,
		stdout:     bufio.NewReader(stdout),
	}

	exe.Run("export TERM=xterm; export LANG=en_US.UTF-8")

	return exe, nil
}

func (s *SSHExecutor) Run(cmdStr string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.Contains(cmdStr, "local_run ") {
		realCmd := strings.ReplaceAll(cmdStr, "local_run ", "")
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/c", realCmd)
		} else {
			cmd = exec.Command("sh", "-c", realCmd+" 2>&1")
		}

		out, err := cmd.CombinedOutput()

		if runtime.GOOS == "windows" {
			if utf8Out, transformErr := GbkToUtf8(out); transformErr == nil {
				out = utf8Out
			}
		}

		outputStr := string(out)
		outputStr = strings.ReplaceAll(outputStr, "Active code page: 65001\r\n", "")

		if err != nil {
			return fmt.Sprintf("💻 [Local Exec Error]: %v\nOutput:\n%s", err, outputStr), nil
		}
		return fmt.Sprintf("💻 [Local Exec Success]:\n%s", outputStr), nil
	}

	if strings.HasPrefix(cmdStr, "upload ") {
		parts := strings.Fields(cmdStr)
		if len(parts) != 3 {
			return "", fmt.Errorf("用法: upload <本地文件> <远程路径>")
		}
		return s.uploadFile(parts[1], parts[2])
	}

	if strings.HasPrefix(cmdStr, "download ") {
		parts := strings.Fields(cmdStr)
		if len(parts) != 3 {
			return "", fmt.Errorf("用法: download <远程文件> <本地路径>")
		}
		return s.downloadFile(parts[1], parts[2])
	}

	endMarker := fmt.Sprintf("__END_%d__", time.Now().UnixNano())
	fullCmd := fmt.Sprintf("%s; echo \"\"; echo \"%s:$?\"\n", cmdStr, endMarker)

	if _, err := s.stdin.Write([]byte(fullCmd)); err != nil {
		return "", fmt.Errorf("写入命令失败: %v", err)
	}

	var outputLines []string
	for {
		line, err := s.stdout.ReadString('\n')
		if err != nil {
			return strings.Join(outputLines, ""), fmt.Errorf("读取中断: %v", err)
		}
		if strings.Contains(line, endMarker) {
			break
		}
		outputLines = append(outputLines, line)
	}

	return strings.TrimSpace(strings.Join(outputLines, "")), nil
}

func (s *SSHExecutor) uploadFile(localPath, remotePath string) (string, error) {
	srcFile, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("无法打开本地文件: %v", err)
	}
	defer srcFile.Close()

	s.sftpClient.MkdirAll(filepath.Dir(remotePath))

	dstFile, err := s.sftpClient.Create(remotePath)
	if err != nil {
		return "", fmt.Errorf("无法创建远程文件: %v", err)
	}
	defer dstFile.Close()

	n, err := io.Copy(dstFile, srcFile)
	if err != nil {
		return "", fmt.Errorf("上传传输失败: %v", err)
	}
	return fmt.Sprintf("✅ 上传成功 (Bytes: %d): %s -> %s", n, localPath, remotePath), nil
}

func (s *SSHExecutor) downloadFile(remotePath, localPath string) (string, error) {
	srcFile, err := s.sftpClient.Open(remotePath)
	if err != nil {
		return "", fmt.Errorf("无法打开远程文件: %v", err)
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return "", fmt.Errorf("创建本地目录失败: %v", err)
	}

	dstFile, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("无法创建本地文件: %v", err)
	}
	defer dstFile.Close()

	n, err := io.Copy(dstFile, srcFile)
	if err != nil {
		return "", fmt.Errorf("下载传输失败: %v", err)
	}
	return fmt.Sprintf("✅ 下载成功 (Bytes: %d): %s -> %s", n, remotePath, localPath), nil
}

func (s *SSHExecutor) IsRemote() bool { return true }

func (s *SSHExecutor) Close() {
	if s.sftpClient != nil {
		s.sftpClient.Close()
	}
	if s.session != nil {
		s.session.Close()
	}
	if s.client != nil {
		s.client.Close()
	}
}

// GbkToUtf8 核心转码函数：将 GBK 转换为 UTF-8
func GbkToUtf8(s []byte) ([]byte, error) {
	reader := transform.NewReader(bytes.NewReader(s), simplifiedchinese.GBK.NewDecoder())
	d, e := io.ReadAll(reader)
	if e != nil {
		return s, e
	}
	return d, nil
}
