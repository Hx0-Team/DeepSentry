package executor

import (
	"ai-edr/internal/config"
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestTelnetFragmentedNegotiationSurvivesReadTimeout(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ex := &TelnetExecutor{conn: client, reader: bufio.NewReader(client)}
	go func() {
		server.Write([]byte{telnetIAC})
		time.Sleep(50 * time.Millisecond)
		server.Write([]byte{telnetWILL, 1})
		reply := make([]byte, 3)
		io.ReadFull(server, reply)
		if !bytes.Equal(reply, []byte{telnetIAC, telnetDO, 1}) {
			t.Error("echo negotiation reply incorrect")
		}
		server.Write([]byte("Login:"))
	}()
	client.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := ex.readDataByte(); err == nil {
		t.Fatal("expected split control timeout")
	}
	client.SetReadDeadline(time.Now().Add(time.Second))
	value, err := ex.readDataByte()
	if err != nil || value != 'L' {
		t.Fatalf("control sequence leaked into prompt: %d %v", value, err)
	}
}

func TestTelnetTerminalTypeAndRefusalHaveNoLoop(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ex := &TelnetExecutor{conn: client, reader: bufio.NewReader(client)}
	done := make(chan []byte, 1)
	go func() {
		server.Write([]byte{telnetIAC, telnetDO, 24})
		reply := make([]byte, 3)
		io.ReadFull(server, reply)
		server.Write([]byte{telnetIAC, telnetSB, 24, 1, telnetIAC, telnetSE})
		terminal := make([]byte, 11)
		io.ReadFull(server, terminal)
		done <- append(reply, terminal...)
		server.Write([]byte{telnetIAC, telnetDONT, 99, '>'})
	}()
	client.SetReadDeadline(time.Now().Add(time.Second))
	value, err := ex.readDataByte()
	if err != nil || value != '>' {
		t.Fatal("terminal negotiation stalled", err)
	}
	reply := <-done
	if !bytes.Contains(reply, []byte("xterm")) || !bytes.Equal(reply[:3], []byte{telnetIAC, telnetWILL, 24}) {
		t.Fatal("terminal response invalid")
	}
}

func TestFTPProxyRouteIsCapturedAndDataResourcesReleased(t *testing.T) {
	server := startFTPCompatServer(t, ftpCompatOptions{epsv: true})
	defer server.Close()
	old := config.GlobalConfig
	defer func() { config.GlobalConfig = old }()
	ex, err := newFTPExecutor(config.Config{FTPHost: server.addr, FTPUser: "lab", FTPPassword: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	config.GlobalConfig.ControllerProxy = "http://127.0.0.1:1"
	for i := 0; i < 3; i++ {
		if _, err := ex.ReadTargetFile("hello.txt"); err != nil {
			t.Fatal("global proxy change redirected established FTP", err)
		}
	}
	n := 0
	ex.resources.Range(func(k, v any) bool { n++; return true })
	if n != 0 {
		t.Fatalf("FTP retained %d completed transfer resources", n)
	}
}

func TestFTPStopClosesDataWithoutWaitingForCommandMutex(t *testing.T) {
	control, controlPeer := net.Pipe()
	defer controlPeer.Close()
	data, dataPeer := net.Pipe()
	defer dataPeer.Close()
	ex := &FTPExecutor{conn: control, reader: bufio.NewReader(control)}
	ex.trackFTPResource(data)
	ex.mu.Lock()
	defer ex.mu.Unlock()
	done := make(chan struct{})
	go func() { ex.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("FTP Close waited for transfer mutex")
	}
	dataPeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := dataPeer.Read(make([]byte, 1)); err == nil {
		t.Fatal("FTP data socket remains open")
	}
}
