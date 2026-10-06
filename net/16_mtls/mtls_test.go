package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// setupMTLS：起 mTLS 服务端（RequireAndVerifyClientCert），返回地址、CA 与对端 CN 通道。
func setupMTLS(t *testing.T) (string, *CA, chan string) {
	t.Helper()
	ca, err := newCA("hands-on mTLS CA", 3000)
	if err != nil {
		t.Fatalf("newCA: %v", err)
	}
	srvCert, err := ca.signCert(serverCertTemplate("mtls-server", 3001))
	if err != nil {
		t.Fatalf("sign server cert: %v", err)
	}
	peerCN := make(chan string, 8)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", mTLSServerConfig(srvCert, ca.Cert))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tlsConn := c.(*tls.Conn)
				if err := tlsConn.Handshake(); err != nil {
					return // 握手失败：无证/假证客户端（服务端视角）
				}
				certs := tlsConn.ConnectionState().PeerCertificates
				if len(certs) == 0 {
					return
				}
				select {
				case peerCN <- certs[0].Subject.CommonName:
				default:
				}
				fmt.Fprintf(c, "hello %s\n", certs[0].Subject.CommonName)
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), ca, peerCN
}

// dialAndRead：客户端拨号 + 握手 + 读一次响应（TLS 1.3 服务端拒绝的感知点）。
func dialAndRead(t *testing.T, addr string, cfg *tls.Config) (string, error) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", addr, cfg)
	if err != nil {
		return "", err
	}
	if err := conn.Handshake(); err != nil {
		return "", err
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

// TestNoCertificateRejected：验收①——无证书客户端被拒
// （客户端侧表现为 remote error: certificate required）。
func TestNoCertificateRejected(t *testing.T) {
	addr, ca, _ := setupMTLS(t)
	_, err := dialAndRead(t, addr, &tls.Config{
		RootCAs: rootsOf(ca), ServerName: "localhost",
	})
	if err == nil {
		t.Fatal("无证书客户端竟完成通信")
	}
	if !strings.Contains(err.Error(), "certificate required") &&
		!strings.Contains(err.Error(), "bad certificate") {
		t.Fatalf("err = %v, want certificate required / bad certificate", err)
	}
}

// TestValidClientVerified：验收②——正确客户端证书握手通过，
// 服务端提取对端 CN 并回话确认。
func TestValidClientVerified(t *testing.T) {
	addr, ca, peerCN := setupMTLS(t)
	good, err := ca.signCert(clientCertTemplate("alice", 3002))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	resp, err := dialAndRead(t, addr, &tls.Config{
		RootCAs:      rootsOf(ca),
		ServerName:   "localhost",
		Certificates: []tls.Certificate{good},
	})
	if err != nil {
		t.Fatalf("正确证书被拒: %v", err)
	}
	if !strings.Contains(resp, "hello alice") {
		t.Fatalf("服务端回话 %q, want hello alice", resp)
	}
	select {
	case cn := <-peerCN:
		if cn != "alice" {
			t.Fatalf("服务端提取的 CN = %q, want alice", cn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("服务端未上报对端 CN")
	}
}

// TestRogueCANotTrusted：验收③——错误 CA 签发的客户端证书被拒
// （ClientCAs 只信任本体系 CA）。
func TestRogueCANotTrusted(t *testing.T) {
	addr, ca, _ := setupMTLS(t)
	rogueCA, err := newCA("rogue CA", 3999)
	if err != nil {
		t.Fatalf("newCA rogue: %v", err)
	}
	rogue, err := rogueCA.signCert(clientCertTemplate("mallory", 3998))
	if err != nil {
		t.Fatalf("sign rogue: %v", err)
	}
	_, err = dialAndRead(t, addr, &tls.Config{
		RootCAs:      rootsOf(ca), // 服务端证书仍可验证
		ServerName:   "localhost",
		Certificates: []tls.Certificate{rogue},
	})
	if err == nil {
		t.Fatal("rogue CA 客户端证书被接受")
	}
}

// TestBothDirectionsVerified：验收④——mTLS 双向验证同场成立：
// 服务端验证客户端（CN 上报），客户端验证服务端证书链。
func TestBothDirectionsVerified(t *testing.T) {
	addr, ca, peerCN := setupMTLS(t)
	good, err := ca.signCert(clientCertTemplate("bob", 3003))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second},
		"tcp", addr, &tls.Config{
			RootCAs:      rootsOf(ca),
			ServerName:   "localhost",
			Certificates: []tls.Certificate{good},
		})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	// 客户端视角：服务端证书链可验证
	if err := conn.Handshake(); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if len(conn.ConnectionState().PeerCertificates) == 0 {
		t.Fatal("客户端未收到服务端证书")
	}
	// 读一次确认应用层联通
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(line, "bob") {
		t.Fatalf("回话 %q 未确认对端 CN", line)
	}
	select {
	case cn := <-peerCN:
		if cn != "bob" {
			t.Fatalf("服务端侧 CN = %q", cn)
		}
	default:
	}
}
