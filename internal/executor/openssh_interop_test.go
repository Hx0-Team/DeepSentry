package executor

import (
	"ai-edr/internal/config"
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestIndependentOpenSSHLegacyCipherInterop(t *testing.T) {
	if os.Getenv("DEEPSENTRY_OPENSSH_INTEROP") != "1" {
		t.Skip("explicit local OpenSSH fixture only")
	}
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		sshd = "/usr/sbin/sshd"
		if _, err = os.Stat(sshd); err != nil {
			t.Skip("system sshd unavailable")
		}
	}
	dir := t.TempDir()
	hostKey := filepath.Join(dir, "host_rsa")
	clientKey := filepath.Join(dir, "client_rsa")
	writeTestRSAKey(t, hostKey, "")
	writeTestRSAKey(t, clientKey, "")
	hostSigner, err := parseSSHPrivateKey(hostKey, "")
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := parseSSHPrivateKey(clientKey, "")
	if err != nil {
		t.Fatal(err)
	}
	authorized := filepath.Join(dir, "authorized_keys")
	os.WriteFile(authorized, ssh.MarshalAuthorizedKey(clientSigner.PublicKey()), 0600)
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	configuration := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %q\nPidFile %q\nAuthorizedKeysFile %q\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nAllowUsers %s\nKexAlgorithms diffie-hellman-group1-sha1\nHostKeyAlgorithms ssh-rsa\nPubkeyAcceptedAlgorithms ssh-rsa\nCiphers aes256-cbc\nMACs hmac-md5\nSubsystem sftp internal-sftp\nForceCommand internal-sftp\nLogLevel ERROR\n", port, hostKey, filepath.Join(dir, "sshd.pid"), authorized, account.Username)
	configPath := filepath.Join(dir, "sshd_config")
	os.WriteFile(configPath, []byte(configuration), 0600)
	var diagnostics bytes.Buffer
	process := exec.Command(sshd, "-D", "-e", "-f", configPath)
	process.Stderr = &diagnostics
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { process.Process.Kill(); process.Wait() }()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ready := false
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("independent sshd did not start: %s", diagnostics.String())
	}
	known := filepath.Join(dir, "known_hosts")
	os.WriteFile(known, []byte(knownhosts.Line([]string{knownhosts.Normalize(addr)}, hostSigner.PublicKey())+"\n"), 0600)
	ex, err := newSFTPExecutor(config.Config{SSHHost: addr, SSHUser: account.Username, SSHKeyPath: clientKey, SSHHostKeyPolicy: "strict", SSHKnownHostsPath: known, SSHConnectTimeoutSec: 5, SSHCommandTimeoutSec: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	alg := ex.ssh.Conn.(ssh.AlgorithmsConnMetadata).Algorithms()
	if alg.KeyExchange != ssh.InsecureKeyExchangeDH1SHA1 || alg.Read.Cipher != ssh.InsecureCipherAES256CBC || alg.Read.MAC != ssh.InsecureHMACMD5 || alg.HostKey != ssh.KeyAlgoRSA {
		t.Fatal("unexpected external negotiation", alg)
	}
	remote := filepath.Join(dir, "external-roundtrip.bin")
	payload := bytes.Repeat([]byte{0, 255, 13, 10, 77}, 16000)
	if err = WriteFileWithExecutor(ex, remote, payload); err != nil {
		t.Fatal(err)
	}
	got, err := ex.ReadTargetFile(remote)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("independent SFTP roundtrip mismatch", err)
	}
	t.Logf("independent server=%s kex=%s cipher=%s mac=%s hostkey=%s verified_bytes=%d", ex.ssh.ServerVersion(), alg.KeyExchange, alg.Read.Cipher, alg.Read.MAC, alg.HostKey, len(payload))
}
