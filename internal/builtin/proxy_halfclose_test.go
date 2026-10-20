package builtin

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProxyHalfCloseDeliversFinalResponseAndBufferedCONNECT(t *testing.T) {
	for _, kind := range []string{"tcp_forward", "http_proxy", "socks5_proxy"} {
		t.Run(kind, func(t *testing.T) {
			target, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			peerDone := make(chan struct{})
			go func() {
				defer close(peerDone)
				peer, err := target.Accept()
				if err != nil {
					return
				}
				defer peer.Close()
				_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
				body, err := io.ReadAll(peer)
				if err == nil {
					_, _ = peer.Write(append([]byte("FINAL:"), body...))
				}
			}()
			host, port, _ := net.SplitHostPort(target.Addr().String())
			switch kind {
			case "tcp_forward":
				_, err = startForward(Runtime{}, "127.0.0.1", "0", host, port)
			case "http_proxy":
				_, err = startHTTPProxy(Runtime{}, "127.0.0.1", "0", "", "", false)
			case "socks5_proxy":
				_, err = startSocks5Proxy(Runtime{}, "127.0.0.1", "0", "", "", false)
			}
			if err != nil {
				t.Fatal(err)
			}
			listen := findForwardListen(t, kind)
			defer func() { _, _ = stopForward(Runtime{}, listenPort(listen)) }()
			var client net.Conn
			if kind == "socks5_proxy" {
				client = dialSocks(t, listen)
				p, _ := strconv.Atoi(port)
				socksCommand(t, client, 1, net.ParseIP(host), p)
			} else {
				client, err = net.DialTimeout("tcp", listen, time.Second)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer client.Close()
			_ = client.SetDeadline(time.Now().Add(3 * time.Second))
			reader := bufio.NewReader(client)
			if kind == "http_proxy" { // payload in the same write proves buffered bytes survive hijack
				_, err = fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\npayload", target.Addr(), target.Addr())
				if err != nil {
					t.Fatal(err)
				}
				response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
				if err != nil || response.StatusCode != 200 {
					t.Fatal("connect", err)
				}
			} else {
				_, _ = client.Write([]byte("payload"))
			}
			if err := client.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			response, err := io.ReadAll(reader)
			if err != nil || string(response) != "FINAL:payload" {
				t.Fatalf("final reply=%q err=%v", response, err)
			}
			<-peerDone
		})
	}
}

func TestForwardStopCancelsFutureDialImmediately(t *testing.T) {
	sess := &forwardSession{Listener: &dummyListener{}}
	sess.context()
	sess.stop()
	start := time.Now()
	_, err := sess.dial("192.0.2.1:1", 10*time.Second)
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("stopped forward dial did not cancel")
	}
}

type dummyListener struct{}

func (*dummyListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (*dummyListener) Close() error              { return nil }
func (*dummyListener) Addr() net.Addr            { return &net.TCPAddr{} }

func TestHTTPProxyIgnoresAmbientProxyAndTracksDirectHTTP(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	probe := newHTTPProxyHandler(&forwardSession{}, "", "")
	defer probe.transport.CloseIdleConnections()
	if probe.transport.Proxy != nil {
		t.Fatal("ambient proxy inherited")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "DIRECT_OK") })}
	go server.Serve(listener)
	defer server.Close()
	_, err = startHTTPProxy(Runtime{}, "127.0.0.1", "0", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	listen := findForwardListen(t, "http_proxy")
	defer func() { _, _ = stopForward(Runtime{}, listenPort(listen)) }()
	conn, err := net.Dial("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", listener.Addr(), listener.Addr())
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), "DIRECT_OK") {
		t.Fatal("ambient proxy affected origin request")
	}
}
