package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// startTLSServer：起自签 TLS echo 服务端，返回地址、叶子证书与关闭函数。
func startTLSServer(t *testing.T, extra func(*tls.Config)) (string, *x509.Certificate, func()) {
	t.Helper()
	cert := genSelfSigned()
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	if extra != nil {
		extra(cfg)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
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
				io.WriteString(c, "hello over TLS\n")
				c.Close()
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), cert.Leaf, func() {}
}

// TestHandshakeWithTrustedPool：验收①——信任池客户端完成握手，
// ConnectionState 的版本/套件/对端证书链全部可读。
func TestHandshakeWithTrustedPool(t *testing.T) {
	addr, leaf, _ := startTLSServer(t, nil)
	conn, err := tls.Dial("tcp", addr, trustedClient(leaf))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	cs := conn.ConnectionState()

	if !cs.HandshakeComplete {
		t.Fatal("HandshakeComplete = false")
	}
	if ver := tls.VersionName(cs.Version); ver != "TLS 1.3" {
		t.Errorf("协议版本 = %s, want TLS 1.3（现代默认）", ver)
	}
	if len(cs.PeerCertificates) == 0 {
		t.Fatal("对端证书链为空")
	}
	leafGot := cs.PeerCertificates[0]
	if !leafGot.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("叶子 SAN IP = %v, want 127.0.0.1", leafGot.IPAddresses)
	}
	if !leafGot.Equal(leaf) {
		t.Error("对端证书与服务端叶子不一致")
	}
}

// TestUnknownAuthorityRejected：验收③——默认信任池拒绝自签证书，
// 错误链里应能找到 x509.UnknownAuthorityError。
func TestUnknownAuthorityRejected(t *testing.T) {
	addr, _, _ := startTLSServer(t, nil)
	_, err := tls.Dial("tcp", addr, &tls.Config{}) // 系统根存储：无此自签证书
	if err == nil {
		t.Fatal("默认信任池竟接受了自签证书")
	}
	var uaErr x509.UnknownAuthorityError
	if !errors.As(err, &uaErr) {
		t.Fatalf("err = %v (%T), want 错误链含 x509.UnknownAuthorityError", err, err)
	}
}

// TestInsecureSkipVerify：跳过验证能握手成功——危险开关的存在证明。
func TestInsecureSkipVerify(t *testing.T) {
	addr, _, _ := startTLSServer(t, nil)
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("InsecureSkipVerify dial: %v", err)
	}
	defer conn.Close()
	if !conn.ConnectionState().HandshakeComplete {
		t.Fatal("握手未完成")
	}
}

// TestKeyLogFile：验收①的补强——KeyLogWriter 收到 TLS 1.3 密钥材料
// （Wireshark 解密流量的依据），写出的行以 CLIENT_HANDSHAKE 开头。
func TestKeyLogFile(t *testing.T) {
	var keyLog bytes.Buffer
	addr, _, _ := startTLSServer(t, func(cfg *tls.Config) {
		cfg.KeyLogWriter = &keyLog
	})
	conn, err := tls.Dial("tcp", addr, trustedClient(genSelfSigned().Leaf))
	if err != nil {
		// 服务端与客户端证书不同但 KeyLog 与证书无关；换成信任池
		roots := x509.NewCertPool()
		roots.AddCert(rootOf(t, addr))
		conn, err = tls.Dial("tcp", addr, &tls.Config{RootCAs: roots, KeyLogWriter: &keyLog})
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
	}
	io.Copy(io.Discard, conn)
	conn.Close()
	time.Sleep(100 * time.Millisecond)

	out := keyLog.String()
	if !strings.Contains(out, "CLIENT_HANDSHAKE_TRAFFIC_SECRET") {
		t.Fatalf("KeyLog 未包含握手密钥行：%q", out)
	}
}

// rootOf：从运行中的 TLS 服务端抓叶子证书（用于构造信任池）。
func rootOf(t *testing.T, addr string) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("rootOf dial: %v", err)
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		t.Fatal("无对端证书")
	}
	return certs[0]
}
