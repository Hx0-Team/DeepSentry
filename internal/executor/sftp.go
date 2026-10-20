package executor

import (
	"ai-edr/internal/config"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type sftpChannel struct {
	client  *sftp.Client
	session *ssh.Session
}

func (s *sftpChannel) Close() {
	if s == nil {
		return
	}
	_ = s.session.Close()
	_ = s.client.Close()
}

// Subsystem setup is bounded. A timeout closes the subsystem and transport,
// since SSH exposes no per-channel-open cancellation. It is not an auth failure.
func openSFTPChannel(client *ssh.Client, timeout time.Duration) (*sftpChannel, error) {
	type result struct {
		value *sftpChannel
		err   error
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan result, 1)
	go func() {
		session, err := client.NewSession()
		if err != nil {
			done <- result{err: err}
			return
		}
		stop := context.AfterFunc(ctx, func() { _ = session.Close() })
		defer stop()
		input, err := session.StdinPipe()
		if err != nil {
			_ = session.Close()
			done <- result{err: err}
			return
		}
		output, err := session.StdoutPipe()
		if err == nil {
			err = session.RequestSubsystem("sftp")
		}
		if err != nil {
			_ = session.Close()
			done <- result{err: err}
			return
		}
		// Sequential requests accommodate older servers with limited windows.
		files, err := sftp.NewClientPipe(output, input, sftp.UseConcurrentReads(false), sftp.UseConcurrentWrites(false))
		if err != nil {
			_ = session.Close()
			done <- result{err: err}
			return
		}
		value := &sftpChannel{client: files, session: session}
		if ctx.Err() != nil {
			value.Close()
			done <- result{err: ctx.Err()}
			return
		}
		done <- result{value: value}
	}()
	select {
	case r := <-done:
		return r.value, r.err
	case <-ctx.Done():
		cancel() // release blocked request reads; transport close also ends a stuck open
		_ = client.Close()
		r := <-done
		if r.value != nil {
			r.value.Close()
		}
		return nil, fmt.Errorf("SFTP 子系统初始化超时 (%s)", timeout)
	}
}

type SFTPExecutor struct {
	mu      sync.Mutex
	ssh     *ssh.Client
	files   *sftpChannel
	timeout time.Duration
}

func (s *SSHExecutor) ensureSFTP() error {
	s.sftpMu.Lock()
	defer s.sftpMu.Unlock()
	if s.sftpClient != nil {
		return nil
	}
	if s.sftpAttempted {
		return s.sftpInitErr
	}
	s.sftpAttempted = true
	if s.client == nil {
		s.sftpInitErr = fmt.Errorf("SSH 连接未初始化，SFTP 不可用")
		return s.sftpInitErr
	}
	timeout := 10 * time.Second
	if s.connectionConfig != nil {
		timeout = sshConnectTimeout(*s.connectionConfig)
	}
	files, err := openSFTPChannel(s.client, timeout)
	if err != nil {
		s.sftpInitErr = fmt.Errorf("该 SSH 服务没有可用 SFTP 子系统，不能执行 upload/download: %w", err)
		return s.sftpInitErr
	}
	s.sftpClient = files.client
	s.sftpSession = files.session
	return nil
}

func (s *SSHExecutor) sftpAdapter() *SFTPExecutor {
	timeout := 90 * time.Second
	if s.connectionConfig != nil {
		timeout = secondsOrDefault(s.connectionConfig.SSHCommandTimeoutSec, 90)
	}
	return &SFTPExecutor{ssh: s.client, files: &sftpChannel{client: s.sftpClient, session: s.sftpSession}, timeout: timeout}
}
func newSFTPExecutor(cfg config.Config) (*SFTPExecutor, error) {
	client, err := dialSSHClient(cfg)
	if err != nil {
		return nil, err
	}
	files, err := openSFTPChannel(client, sshConnectTimeout(cfg))
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("SSH 认证后 SFTP 子系统不可用: %w", err)
	}
	return &SFTPExecutor{ssh: client, files: files, timeout: secondsOrDefault(cfg.SSHCommandTimeoutSec, 90)}, nil
}
func (s *SFTPExecutor) IsRemote() bool { return true }
func (s *SFTPExecutor) Mode() string   { return "sftp" }
func (s *SFTPExecutor) Close() {
	if s == nil {
		return
	}
	if s.ssh != nil {
		_ = s.ssh.Close()
	}
	s.files.Close()
}

func sftpOperation[T any](s *SFTPExecutor, operation func(*sftp.Client) (T, error)) (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() { value, err := operation(s.files.client); done <- result{value, err} }()
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.value, r.err
	case <-timer.C:
		s.Close()
		r := <-done
		return r.value, fmt.Errorf("SFTP 操作超时，连接已关闭；已发送的写入不自动重放")
	}
}
func (s *SFTPExecutor) ReadTargetFile(remote string) ([]byte, error) {
	return sftpOperation(s, func(c *sftp.Client) ([]byte, error) {
		f, err := c.Open(remote)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxReadSize+1))
		if len(data) > maxReadSize {
			return nil, fmt.Errorf("SFTP 文件超过读取窗口 %d 字节；使用下载工具", maxReadSize)
		}
		return data, err
	})
}
func (s *SFTPExecutor) ListTargetDir(remote string) ([]string, error) {
	return sftpOperation(s, func(c *sftp.Client) ([]string, error) {
		entries, err := c.ReadDir(remote)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names, err
	})
}
func (s *SFTPExecutor) readEntries(remote string) ([]DirEntry, error) {
	return sftpOperation(s, func(c *sftp.Client) ([]DirEntry, error) {
		entries, err := c.ReadDir(remote)
		var out []DirEntry
		for _, e := range entries {
			out = append(out, DirEntry{Name: e.Name(), Size: e.Size(), IsDir: e.IsDir(), Mode: e.Mode(), ModTime: e.ModTime()})
		}
		return out, err
	})
}
func (s *SFTPExecutor) writeTargetFile(remote string, content []byte) error {
	_, err := sftpOperation(s, func(c *sftp.Client) (int, error) {
		if err := c.MkdirAll(path.Dir(remote)); err != nil {
			return 0, err
		}
		f, err := c.Create(remote)
		if err != nil {
			return 0, err
		}
		n, writeErr := f.Write(content)
		closeErr := f.Close()
		if writeErr != nil {
			return n, writeErr
		}
		return n, closeErr
	})
	return err
}
func (s *SFTPExecutor) Run(command string) (string, error) {
	args, err := SplitCommandArguments(command)
	if err != nil || len(args) == 0 {
		return "", fmt.Errorf("SFTP 命令无效")
	}
	switch strings.ToLower(args[0]) {
	case "upload", "download":
		if len(args) != 3 {
			return "", fmt.Errorf("SFTP 传输使用 upload <本地> <远程> 或 download <远程> <本地>")
		}
		return s.transfer(args[0], args[1], args[2])
	case "ls", "list":
		remote := "."
		if len(args) > 1 {
			remote = args[1]
		}
		names, err := s.ListTargetDir(remote)
		return strings.Join(names, "\n"), err
	case "pwd":
		return sftpOperation(s, func(c *sftp.Client) (string, error) { return c.Getwd() })
	default:
		return "", fmt.Errorf("SFTP 是文件传输模式，不提供远程 Shell；使用 ls/pwd/upload/download 或文件工具")
	}
}
func (s *SFTPExecutor) transfer(action, from, to string) (string, error) {
	n, err := sftpOperation(s, func(c *sftp.Client) (int64, error) {
		if strings.EqualFold(action, "upload") {
			local, err := os.Open(expandLocalPath(from))
			if err != nil {
				return 0, err
			}
			defer local.Close()
			if err := c.MkdirAll(path.Dir(to)); err != nil {
				return 0, err
			}
			remote, err := c.Create(to)
			if err != nil {
				return 0, err
			}
			n, copyErr := io.Copy(remote, local)
			closeErr := remote.Close()
			if copyErr != nil {
				return n, copyErr
			}
			return n, closeErr
		}
		remote, err := c.Open(from)
		if err != nil {
			return 0, err
		}
		defer remote.Close()
		localPath := expandLocalPath(to)
		if err := os.MkdirAll(filepath.Dir(localPath), 0700); err != nil {
			return 0, err
		}
		temp, err := os.CreateTemp(filepath.Dir(localPath), ".deepsentry-sftp-*")
		if err != nil {
			return 0, err
		}
		defer os.Remove(temp.Name())
		_ = temp.Chmod(0600)
		n, copyErr := io.Copy(temp, remote)
		closeErr := temp.Close()
		if copyErr != nil {
			return n, copyErr
		}
		if closeErr != nil {
			return n, closeErr
		}
		return n, replaceDownloadedFile(temp.Name(), localPath)
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SFTP %s 完成，bytes=%d", action, n), nil
}
