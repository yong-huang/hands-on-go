package main

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// setupDiag：签发根+中间+好叶子，返回三件套。
func setupDiag(t *testing.T) (*CA, *CA, tls.Certificate) {
	t.Helper()
	root, err := newCA("diag Root CA", 4000)
	if err != nil {
		t.Fatalf("newCA: %v", err)
	}
	interCAObj, err := newCA("diag Intermediate CA", 4001)
	if err != nil {
		t.Fatalf("newCA intermediate: %v", err)
	}
	// 用根给中间 CA 证书重签一张由根背书的（保持链结构）
	interTpl := intermediateTemplateOf("diag Intermediate CA", 4001)
	interCert, err := root.signCert(interTpl)
	if err != nil {
		t.Fatalf("sign intermediate: %v", err)
	}
	_ = interCAObj
	inter := &CA{Cert: interCert.Leaf, Key: interCert.PrivateKey.(*ecdsa.PrivateKey)}
	leaf, err := inter.signCert(leafTemplate("diag-server", 4002,
		[]net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"},
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("sign leaf: %v", err)
	}
	return root, inter, leaf
}

// startDiagSrv：按 fullChain 起服务端，返回地址与关闭函数。
func startDiagSrv(fullChain bool, leaf, inter tls.Certificate) (string, func()) {
	return startSrv(fullChain, leaf, inter)
}

// unwrapAll：展开错误链为字符串切片。
func unwrapAll(err error) []string {
	var out []string
	for e := err; e != nil; {
		out = append(out, fmtSprint(e))
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return out
}

// intermediateTemplateOf：中间 CA 模板（与 main.go 同构）。
func intermediateTemplateOf(name string, serial int64) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"hands-on-net"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
}

// tlsCertOf：中间 CA 的 tls.Certificate。
func tlsCertOf(inter *CA) tls.Certificate {
	return tls.Certificate{
		Certificate: [][]byte{inter.Cert.Raw},
		PrivateKey:  inter.Key,
		Leaf:        inter.Cert,
	}
}

// probeErr：连一次只留错误。
func probeErr(addr string, cfg *tls.Config) error {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", addr, cfg)
	if err == nil {
		conn.Close()
		return nil
	}
	return err
}

// fmtSprint：避免直接依赖 fmt.Sprint 的别名噪音。
func fmtSprint(v any) string { return fmt.Sprintf("%T: %v", v, v) }

// TestExpiredLeafDiagnosis：验收①——过期证书的可复现现场与错误分诊。
func TestExpiredLeafDiagnosis(t *testing.T) {
	root, inter, _ := setupDiag(t)
	leaf, err := inter.signCert(leafTemplate("expired", 4100,
		[]net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"},
		time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	addr, closeSrv := startDiagSrv(true, leaf, tlsCertOf(inter))
	defer closeSrv()

	err = probeErr(addr, trustRoot(root, nil))
	var invErr x509.CertificateInvalidError
	if !errors.As(err, &invErr) || invErr.Reason != x509.Expired {
		t.Fatalf("err = %v, want CertificateInvalidError(Expired)", err)
	}
	chain := unwrapAll(err)
	joined := strings.Join(chain, "\n")
	if !strings.Contains(joined, "CertificateVerificationError") {
		t.Fatalf("错误链缺 CertificateVerificationError 包装：%v", chain)
	}
	t.Logf("Go 错误链：%v", chain)
}

// TestSANMismatchDiagnosis：验收②——SAN 错配现场的分诊。
func TestSANMismatchDiagnosis(t *testing.T) {
	root, inter, _ := setupDiag(t)
	leaf, err := inter.signCert(leafTemplate("wrong-san", 4101,
		[]net.IP{net.ParseIP("10.0.0.1")}, []string{"elsewhere.example"},
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	addr, closeSrv := startDiagSrv(true, leaf, tlsCertOf(inter))
	defer closeSrv()

	err = probeErr(addr, trustRoot(root, nil))
	// HostnameError 被 tls.CertificateVerificationError 包装且文案分两态
	//（"is valid for X, not Y" 与 "doesn't contain any IP SANs"）——按文案分诊。
	msg := err.Error()
	if !strings.Contains(msg, "is valid for") && !strings.Contains(msg, "IP SANs") {
		t.Fatalf("err = %v (%T), want SAN/主机名类拒绝", err, err)
	}
}

// TestIncompleteChainDiagnosis：验收③——链不完整现场的分诊
// （服务端只发叶子；客户端只信根 → UnknownAuthorityError）。
func TestIncompleteChainDiagnosis(t *testing.T) {
	root, inter, leaf := setupDiag(t)
	addr, closeSrv := startDiagSrv(false, leaf, tlsCertOf(inter))
	defer closeSrv()

	err := probeErr(addr, trustRoot(root, nil))
	var uaErr x509.UnknownAuthorityError
	if !errors.As(err, &uaErr) {
		t.Fatalf("err = %v (%T), want UnknownAuthorityError", err, err)
	}
}

// TestClockSkewDiagnosis：验收④——客户端时钟回拨一年，
// 报错为 not yet valid（CertificateInvalidError，Reason 非 Expired）。
func TestClockSkewDiagnosis(t *testing.T) {
	root, inter, leaf := setupDiag(t)
	addr, closeSrv := startDiagSrv(true, leaf, tlsCertOf(inter))
	defer closeSrv()

	aYearAgo := func() time.Time { return time.Now().Add(-365 * 24 * time.Hour) }
	err := probeErr(addr, trustRoot(root, aYearAgo))
	// x509 对 not-yet-valid 与 expired 同报 Reason=Expired——按消息里的
	// before/after 分诊：回拨时钟必然是 "current time ... is before ..."。
	var invErr x509.CertificateInvalidError
	if !errors.As(err, &invErr) {
		t.Fatalf("err = %v (%T), want CertificateInvalidError", err, err)
	}
	if !strings.Contains(err.Error(), " is before ") {
		t.Fatalf("err = %v, want 'current time ... is before ...'（NotBefore 校验）", err)
	}
}

// TestDecisionTreeCoversFaults：决策树覆盖四个现场的报错前缀。
func TestDecisionTreeCoversFaults(t *testing.T) {
	var tree strings.Builder
	printDecisionTreeTo(&tree)
	s := tree.String()
	for _, want := range []string{
		"certificate has expired",
		"is valid for", // SAN 错配
		"unknown authority",
		"not yet valid",        // 时钟偏差
		"certificate required", // mTLS
	} {
		if !strings.Contains(s, want) {
			t.Errorf("决策树缺关键报错：%q", want)
		}
	}
}
