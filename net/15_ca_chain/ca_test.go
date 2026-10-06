package main

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// setupChain：签发完整三级链（根→中间→好叶子），起服务端，返回全套件。
func setupChain(t *testing.T) (root *CA, inter *CA, addr string, closeSrv func()) {
	t.Helper()
	root, err := newCA("hands-on Root CA", 1000)
	if err != nil {
		t.Fatalf("newCA root: %v", err)
	}
	interCert, err := root.signCert(intermediateTemplate("hands-on Intermediate CA", 1001))
	if err != nil {
		t.Fatalf("sign intermediate: %v", err)
	}
	inter = &CA{Cert: interCert.Leaf, Key: interCert.PrivateKey.(*ecdsa.PrivateKey)}
	leaf, err := inter.signCert(leafTemplate("good-service", 2000,
		[]net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"},
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("sign leaf: %v", err)
	}
	addr, closeSrv = startTLSServer(chainConfig(leaf, interCert))
	return root, inter, addr, closeSrv
}

// TestChainVerificationSucceeds：验收①——客户端只信任根，
// 服务端发 [叶子, 中间]，验签自动回溯到根，握手通过且对端链正确。
func TestChainVerificationSucceeds(t *testing.T) {
	root, _, addr, closeSrv := setupChain(t)
	defer closeSrv()

	conn, err := tls.Dial("tcp", addr, trustRoot(root))
	if err != nil {
		t.Fatalf("握手失败: %v", err)
	}
	defer conn.Close()
	cs := conn.ConnectionState()
	if !cs.HandshakeComplete {
		t.Fatal("握手未完成")
	}
	if len(cs.PeerCertificates) != 2 {
		t.Fatalf("对端链 %d 张证书, want 2（叶子+中间；根不发送）", len(cs.PeerCertificates))
	}
	leafGot, interGot := cs.PeerCertificates[0], cs.PeerCertificates[1]
	if leafGot.Subject.CommonName != "good-service" || interGot.Subject.CommonName != "hands-on Intermediate CA" {
		t.Fatalf("对端链 CN = [%s, %s]", leafGot.Subject.CommonName, interGot.Subject.CommonName)
	}
	// 验签路径确证：叶子的签发者 = 中间，中间的签发者 = 根
	if leafGot.Issuer.CommonName != interGot.Subject.CommonName {
		t.Fatalf("叶子 Issuer = %q, want 中间 CA 的 CN", leafGot.Issuer.CommonName)
	}
	if interGot.Issuer.CommonName != root.Cert.Subject.CommonName {
		t.Fatalf("中间 Issuer = %q, want 根 CA 的 CN", interGot.Issuer.CommonName)
	}
}

// TestSANMismatchRejected：验收②——叶子 SAN 不含目标 IP，
// 链验证通过但主机名匹配失败（x509.HostnameError）。
func TestSANMismatchRejected(t *testing.T) {
	root, inter, _, closeSrv := setupChain(t)
	defer closeSrv()

	// SAN 只有 localhost，不含 127.0.0.1
	badSAN, err := inter.signCert(leafTemplate("san-mismatch", 2001,
		nil, []string{"only-localhost.example"},
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	addr, closeBad := startTLSServer(chainConfig(badSAN, interCertOf(inter, t)))
	defer closeBad()

	// ServerName 指向 IP（服务端监听在 127.0.0.1 上，SAN 不含它）
	roots := x509.NewCertPool()
	roots.AddCert(root.Cert)
	_, err = tls.Dial("tcp", addr, &tls.Config{RootCAs: roots, ServerName: "127.0.0.1"})
	if err == nil {
		t.Fatal("SAN 不含目标 IP 却验证通过")
	}
	// Go 对"叶子无 IP SAN"与"SAN 不含该 IP"都报 HostnameError 语义，
	// 但被 tls.CertificateVerificationError 包装且文案分两种——按内容分诊。
	msg := err.Error()
	isHostname := errors.As(err, new(*x509.HostnameError)) ||
		strings.Contains(msg, "doesn't contain any IP SANs") ||
		strings.Contains(msg, "is not valid for")
	if !isHostname {
		t.Fatalf("err = %v (%T), want 主机名/SAN 类拒绝", err, err)
	}
	t.Logf("拒绝理由：%v", err)
}

// TestExpiredLeafRejected：验收③——叶子 NotAfter 在过去，
// 验证失败（证书过期）。
func TestExpiredLeafRejected(t *testing.T) {
	root, inter, _, closeSrv := setupChain(t)
	defer closeSrv()

	expired, err := inter.signCert(leafTemplate("expired-service", 2002,
		[]net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"},
		time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))) // 已过期
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	addr, closeExp := startTLSServer(chainConfig(expired, interCertOf(inter, t)))
	defer closeExp()

	_, err = tls.Dial("tcp", addr, trustRoot(root))
	if err == nil {
		t.Fatal("过期证书验证通过")
	}
	var invErr x509.CertificateInvalidError
	if !errors.As(err, &invErr) || invErr.Reason != x509.Expired {
		t.Fatalf("err = %v (%T), want CertificateInvalidError(Expired)", err, err)
	}
}

// TestIncompleteChainRejected：验收④——服务端只发叶子不发中间，
// 客户端只信任根：链不完整，验证失败（生产最经典的 TLS 故障形态）。
func TestIncompleteChainRejected(t *testing.T) {
	root, inter, _, closeSrv := setupChain(t)
	defer closeSrv()

	leaf, err := inter.signCert(leafTemplate("no-inter-sent", 2003,
		[]net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"},
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// 关键：链里只有叶子，中间证书缺失
	addr, closeBad := startTLSServer(&tls.Config{Certificates: []tls.Certificate{leaf}})
	defer closeBad()

	_, err = tls.Dial("tcp", addr, trustRoot(root))
	if err == nil {
		t.Fatal("链不完整却验证通过")
	}
	var uaErr x509.UnknownAuthorityError
	if !errors.As(err, &uaErr) {
		t.Fatalf("err = %v (%T), want UnknownAuthorityError（叶子签发者不在信任池）", err, err)
	}
	t.Logf("拒绝理由：%v", err)
}

// interCertOf：取中间 CA 的 tls.Certificate（服务端需要发它）。
func interCertOf(inter *CA, t *testing.T) tls.Certificate {
	t.Helper()
	return tls.Certificate{
		Certificate: [][]byte{inter.Cert.Raw},
		PrivateKey:  inter.Key,
		Leaf:        inter.Cert,
	}
}
