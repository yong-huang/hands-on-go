// 项目 15：自建 CA 与证书链——根、中间、叶子，三级签发与四种验证结果。
//
// 项目 14 用"信任叶子"的捷径完成了自签实验；生产 mTLS 的正路是自建 CA：
//
//	根 CA（Root CA）       ：离线保管，只签中间 CA
//	中间 CA（Intermediate）：在线签发叶子；被吊销不殃及根
//	叶子证书（Leaf）       ：服务端身份，SAN 声明主机名/IP
//
// 客户端只信任根；验证沿"叶子 ← 中间 ← 根"逐级回溯。证书链的意义：
// 根不用上线（离线安全）、签发权可下放（中间按用途分权）、吊销不殃及根。
//
// 四个实验：
//
//	第 1 步：脚本化签发三级链，服务端发 [叶子, 中间]，客户端只信根 → 通过
//	第 2 步：叶子 SAN 不含目标 IP → x509.HostnameError
//	第 3 步：叶子 NotAfter 在过去 → 已过期错误
//	第 4 步：服务端只发叶子不发中间 → 链不完整，验证失败（经典真实故障）
//
// 运行方式：go run .；断言见 go test -race。
package main

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

// CA：一个可签发子证书的机构（证书 + 私钥）。
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// newCA：生成一张自签 CA 证书（IsCA=true，KeyUsage 含 CertSign）。
func newCA(name string, serial int64) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"hands-on-net"}},
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

// signCert：用本 CA 给下一级签名。模板由调用方全权定制（SAN/有效期/IsCA）。
func (ca *CA) signCert(tpl *x509.Certificate) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
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

// leafTemplate：叶子证书模板（SAN 与有效期可定制）。
func leafTemplate(cn string, serial int64, ips []net.IP, dns []string, nb, na time.Time) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"hands-on-net"}},
		NotBefore:             nb,
		NotAfter:              na,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           ips,
		DNSNames:              dns,
		BasicConstraintsValid: true,
	}
}

// intermediateTemplate：中间 CA 模板（IsCA=true，MaxPathLen=0 表示不能再签 CA）。
func intermediateTemplate(name string, serial int64) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"hands-on-net"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}
}

// chainConfig：服务端 TLS 配置——Certificate 链 = [叶子, 中间]（根不发送）。
func chainConfig(leaf, inter tls.Certificate) *tls.Config {
	full := leaf
	full.Certificate = [][]byte{leaf.Certificate[0], inter.Certificate[0]}
	return &tls.Config{Certificates: []tls.Certificate{full}}
}

// trustRoot：只信任根 CA 的客户端配置。
func trustRoot(root *CA) *tls.Config {
	roots := x509.NewCertPool()
	roots.AddCert(root.Cert)
	return &tls.Config{RootCAs: roots, ServerName: "localhost"}
}

// startTLSServer：起一个 TLS 服务端（指定证书链），返回地址与关闭函数。
func startTLSServer(cfg *tls.Config) (string, func()) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				fmt.Fprintf(c, "ok\n")
				c.Close()
			}(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func main() {
	root, err := newCA("hands-on Root CA", 1000)
	if err != nil {
		fmt.Println(err)
		return
	}
	interCert, err := root.signCert(intermediateTemplate("hands-on Intermediate CA", 1001))
	if err != nil {
		fmt.Println(err)
		return
	}
	inter := &CA{Cert: interCert.Leaf, Key: interCert.PrivateKey.(*ecdsa.PrivateKey)}
	fmt.Println("== 签发三级链 ==")
	fmt.Printf("  根 CA：CN=%q（自签）\n", root.Cert.Subject.CommonName)
	fmt.Printf("  中间 CA：CN=%q（根签发）\n", inter.Cert.Subject.CommonName)

	// 叶子由中间签，服务端链 = [叶子, 中间]
	good, err := inter.signCert(leafTemplate("good-service", 2000,
		[]net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"},
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour)))
	if err != nil {
		fmt.Println(err)
		return
	}
	addr, closeSrv := startTLSServer(chainConfig(good, interCert))
	defer closeSrv()

	fmt.Println("== 实验第 1 步：只信任根 → 验签自动回溯，握手通过 ==")
	conn, err := tls.Dial("tcp", addr, trustRoot(root))
	if err != nil {
		fmt.Println("  dial:", err)
		return
	}
	cs := conn.ConnectionState()
	fmt.Printf("  握手完成=%v，对端链 %d 张证书：", cs.HandshakeComplete, len(cs.PeerCertificates))
	for _, pc := range cs.PeerCertificates {
		fmt.Printf("[%s] ", pc.Subject.CommonName)
	}
	fmt.Println("（叶子 → 中间；根在客户端信任池里）")
	conn.Close()
	fmt.Println("  ⇒ 链的意义：根离线、签发权下放、吊销不殃及根。")
	fmt.Println("（SAN 错配 / 过期 / 链不完整 三个失败实验见 go test -race）")
}
