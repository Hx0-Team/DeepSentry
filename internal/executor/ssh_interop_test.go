package executor

import (
	"bytes"
	//lint:ignore SA1019 Legacy DSA server fixture verifies opt-in compatibility and strict-mode rejection.
	"crypto/dsa"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-edr/internal/config"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type sshTestProfile struct {
	stallSubsystem         bool
	name, kex, cipher, mac string
	dsa                    bool
	publicKey              ssh.PublicKey
	onlyPublic             bool
}
type sshInteropServer struct {
	addr, root    string
	signer        ssh.Signer
	listener      net.Listener
	connections   sync.Map
	wg            sync.WaitGroup
	shellRequests atomic.Int32
}

func startSSHInteropServer(t *testing.T, p sshTestProfile) *sshInteropServer {
	t.Helper()
	var key any
	if p.dsa {
		var k dsa.PrivateKey
		if err := dsa.GenerateParameters(&k.Parameters, rand.Reader, dsa.L1024N160); err != nil {
			t.Fatal(err)
		}
		if err := dsa.GenerateKey(&k, rand.Reader); err != nil {
			t.Fatal(err)
		}
		key = &k
	} else {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		key = k
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if !p.dsa && p.kex != ssh.KeyExchangeCurve25519 {
		signer, err = ssh.NewSignerWithAlgorithms(signer.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSA})
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := &ssh.ServerConfig{MaxAuthTries: 1, Config: ssh.Config{KeyExchanges: []string{p.kex}, Ciphers: []string{p.cipher}, MACs: []string{p.mac}}, PasswordCallback: func(m ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if !p.onlyPublic && m.User() == "lab" && string(password) == "lab-password" {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}, PublicKeyAuthAlgorithms: []string{ssh.KeyAlgoRSA}, PublicKeyCallback: func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if p.publicKey != nil && bytes.Equal(k.Marshal(), p.publicKey.Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	if p.onlyPublic {
		cfg.PasswordCallback = nil
	}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &sshInteropServer{addr: listener.Addr().String(), listener: listener, root: t.TempDir(), signer: signer}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			s.connections.Store(raw, true)
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer s.connections.Delete(raw)
				defer raw.Close()
				conn, channels, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for request := range channels {
					if request.ChannelType() != "session" {
						_ = request.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					channel, requests, err := request.Accept()
					if err != nil {
						return
					}
					s.wg.Add(1)
					go func() {
						defer s.wg.Done()
						defer channel.Close()
						for r := range requests {
							if r.Type == "subsystem" {
								var payload struct{ Name string }
								_ = ssh.Unmarshal(r.Payload, &payload)
								if payload.Name == "sftp" {
									_ = r.Reply(true, nil)
									if p.stallSubsystem {
										_, _ = io.Copy(io.Discard, channel)
										return
									}
									files, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(s.root))
									if err != nil {
										return
									}
									_ = files.Serve()
									_ = files.Close()
									return
								}
							}
							if r.Type == "shell" || r.Type == "exec" || r.Type == "pty-req" {
								s.shellRequests.Add(1)
							}
							_ = r.Reply(false, nil)
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		s.connections.Range(func(k, v any) bool { k.(net.Conn).Close(); return true })
		s.wg.Wait()
	})
	return s
}

func TestSSHRealNegotiationMatrix(t *testing.T) {
	profiles := []sshTestProfile{
		{name: "group1-aes128-sha1-rsa", kex: ssh.InsecureKeyExchangeDH1SHA1, cipher: ssh.InsecureCipherAES128CBC, mac: ssh.HMACSHA1},
		{name: "group1-aes192-md5-96-dsa", kex: ssh.InsecureKeyExchangeDH1SHA1, cipher: ssh.InsecureCipherAES192CBC, mac: ssh.InsecureHMACMD596, dsa: true},
		{name: "group1-aes256-md5-rsa", kex: ssh.InsecureKeyExchangeDH1SHA1, cipher: ssh.InsecureCipherAES256CBC, mac: ssh.InsecureHMACMD5},
		{name: "group14-sha1-3des-sha1-96", kex: ssh.InsecureKeyExchangeDH14SHA1, cipher: ssh.InsecureCipherTripleDESCBC, mac: ssh.InsecureHMACSHA196},
		{name: "gex-sha1-aes256-md5", kex: ssh.InsecureKeyExchangeDHGEXSHA1, cipher: ssh.InsecureCipherAES256CBC, mac: ssh.InsecureHMACMD5},
		{name: "gex-sha256-aes256-ctr", kex: ssh.KeyExchangeDHGEXSHA256, cipher: ssh.CipherAES256CTR, mac: ssh.HMACSHA256},
		{name: "modern-curve25519-aes-gcm", kex: ssh.KeyExchangeCurve25519, cipher: ssh.CipherAES128GCM, mac: ssh.HMACSHA256},
	}
	var records []map[string]any
	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			server := startSSHInteropServer(t, p)
			cfg := config.Config{SSHHost: server.addr, SSHUser: "lab", SSHPassword: "lab-password", SSHHostKeyPolicy: "insecure", SSHConnectTimeoutSec: 3}
			client, err := dialSSHClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			alg := client.Conn.(ssh.AlgorithmsConnMetadata).Algorithms()
			if alg.KeyExchange != p.kex || alg.Read.Cipher != p.cipher {
				t.Fatal("unexpected negotiated algorithms", alg)
			}
			files, err := openSFTPChannel(client, 3e9)
			if err != nil {
				t.Fatal(err)
			}
			defer files.Close()
			file, err := files.client.Create(filepath.Join(server.root, "encrypted-roundtrip.bin"))
			if err != nil {
				t.Fatal(err)
			}
			payload := bytes.Repeat([]byte{0, 1, 255, 127, 23}, 12000)
			if _, err = file.Write(payload); err != nil {
				t.Fatal(err)
			}
			file.Close()
			file, err = files.client.Open(filepath.Join(server.root, "encrypted-roundtrip.bin"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(file)
			file.Close()
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatal("encrypted transfer mismatch", err)
			}
			records = append(records, map[string]any{"profile": p.name, "kex": alg.KeyExchange, "cipher": alg.Read.Cipher, "host_key": alg.HostKey, "mac": alg.Read.MAC, "encrypted_roundtrip_bytes": len(payload), "passed": true})
		})
	}
	if destination := os.Getenv("DEEPSENTRY_SSH_MATRIX_REPORT"); destination != "" {
		raw, _ := json.MarshalIndent(map[string]any{"server": "local Go SSH2 protocol servers with constrained algorithm profiles; not historical OpenSSH binaries", "profiles": records}, "", "  ")
		if err := os.WriteFile(destination, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSFTPOnlyAccountEndToEndAndNoShell(t *testing.T) {
	server := startSSHInteropServer(t, sshTestProfile{name: "sftp", kex: ssh.InsecureKeyExchangeDH1SHA1, cipher: ssh.InsecureCipherAES256CBC, mac: ssh.InsecureHMACMD5})
	ex, err := newSFTPExecutor(config.Config{SSHHost: server.addr, SSHUser: "lab", SSHPassword: "lab-password", SSHHostKeyPolicy: "insecure", SSHConnectTimeoutSec: 3, SSHCommandTimeoutSec: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	local := filepath.Join(t.TempDir(), "上传 文件.bin")
	payload := bytes.Repeat([]byte{0, 255, 13, 10, 77}, 20000)
	if err := os.WriteFile(local, payload, 0600); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(server.root, "远端 文件.bin")
	if _, err = ex.Run(fmt.Sprintf("upload %q %q", local, remote)); err != nil {
		t.Fatal(err)
	}
	if got, err := ex.ReadTargetFile(remote); err != nil || !bytes.Equal(got, payload) {
		t.Fatal("read mismatch", err)
	}
	entries, err := ReadEntriesWithExecutor(ex, server.root)
	if err != nil || len(entries) != 1 || entries[0].Size != int64(len(payload)) {
		t.Fatal("entry metadata", err)
	}
	download := filepath.Join(t.TempDir(), "下载 文件.bin")
	if _, err = ex.Run(fmt.Sprintf("download %q %q", remote, download)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(download)
	if !bytes.Equal(got, payload) {
		t.Fatal("download mismatch")
	}
	if _, err := ex.Run("uname -a"); err == nil {
		t.Fatal("SFTP accepted shell command")
	}
	if server.shellRequests.Load() != 0 {
		t.Fatal("file mode attempted shell")
	}
	if err := WriteFileWithExecutor(ex, remote, []byte("updated")); err != nil {
		t.Fatal(err)
	}
}

func TestOldRSAUserSignatureAndHostTrust(t *testing.T) {
	keyfile := filepath.Join(t.TempDir(), "legacy-key")
	writeTestRSAKey(t, keyfile, "")
	key, err := parseSSHPrivateKey(keyfile, "")
	if err != nil {
		t.Fatal(err)
	}
	server := startSSHInteropServer(t, sshTestProfile{name: "rsa-auth", kex: ssh.InsecureKeyExchangeDH1SHA1, cipher: ssh.InsecureCipherAES256CBC, mac: ssh.InsecureHMACMD5, publicKey: key.PublicKey(), onlyPublic: true})
	known := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(server.addr)}, server.signer.PublicKey())
	os.WriteFile(known, []byte(line+"\n"), 0600)
	cfg := config.Config{SSHHost: server.addr, SSHUser: "lab", SSHKeyPath: keyfile, SSHHostKeyPolicy: "strict", SSHKnownHostsPath: known, SSHConnectTimeoutSec: 3}
	client, err := dialSSHClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	cfg.SSHLegacyCompat = "false"
	if _, err := dialSSHClient(cfg); err == nil || !strings.Contains(err.Error(), "旧协议兼容关闭") {
		t.Fatal("strict modern mode unexpectedly negotiated legacy", err)
	}
	cfg.SSHLegacyCompat = "true"
	another, _ := rsa.GenerateKey(rand.Reader, 2048)
	wrong, _ := ssh.NewPublicKey(&another.PublicKey)
	os.WriteFile(known, []byte(knownhosts.Line([]string{knownhosts.Normalize(server.addr)}, wrong)+"\n"), 0600)
	if _, err := dialSSHClient(cfg); err == nil {
		t.Fatal("compatibility bypassed host-key mismatch")
	}
}

func TestMissingSFTPReturnsErrorInsteadOfPanicking(t *testing.T) {
	ex := &SSHExecutor{}
	if _, err := ex.uploadFile("/tmp/missing", "/tmp/remote"); err == nil {
		t.Fatal("missing SFTP upload succeeded")
	}
	if _, err := ex.downloadFile("/tmp/remote", "/tmp/missing"); err == nil {
		t.Fatal("missing SFTP download succeeded")
	}
}

func TestSFTPSubsystemInitializationDoesNotHangAfterAuthentication(t *testing.T) {
	server := startSSHInteropServer(t, sshTestProfile{kex: ssh.KeyExchangeCurve25519, cipher: ssh.CipherAES128GCM, mac: ssh.HMACSHA256, stallSubsystem: true})
	client, err := dialSSHClient(config.Config{SSHHost: server.addr, SSHUser: "lab", SSHPassword: "lab-password", SSHHostKeyPolicy: "insecure", SSHConnectTimeoutSec: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	start := time.Now()
	if _, err := openSFTPChannel(client, 100*time.Millisecond); err == nil || time.Since(start) > time.Second {
		t.Fatal("subsystem timeout did not release connection", err)
	}
}
