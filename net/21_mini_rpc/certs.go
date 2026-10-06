package main

// 证书生成：自签 CA + 服务端/客户端叶子（SAN 双写 127.0.0.1 与 localhost）。
// 生产中由外部 CA 签发；此处内存生成保证实验自包含（项目 15 的签发逻辑微缩版）。

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// CA：签发机构。
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

func newCA(name string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"mini-rpc"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key}, nil
}

// sign：按 CommonName 签发叶子（SAN 固定本机回环）。
func (ca *CA) sign(cn string, serial int64, isClient bool) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	usage := x509.ExtKeyUsageServerAuth
	if isClient {
		usage = x509.ExtKeyUsageClientAuth
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"mini-rpc"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{usage},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// serverTLS：mTLS 服务端配置——要求并验证客户端证书。
func serverTLS(ca *CA, srvCert tls.Certificate) *tls.Config {
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(ca.Cert)
	return &tls.Config{
		Certificates: []tls.Certificate{srvCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS12,
	}
}

// clientTLS：mTLS 客户端配置——出示客户端证书并验证服务端链。
func clientTLS(ca *CA, cliCert tls.Certificate) *tls.Config {
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	return &tls.Config{
		RootCAs:      roots,
		Certificates: []tls.Certificate{cliCert},
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS12,
	}
}

var _ = fmt.Sprintf // 占位：部分构建路径未用 fmt
