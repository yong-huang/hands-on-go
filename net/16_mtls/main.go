// 项目 16：mTLS 双向认证——服务端也验客户端的身份。
//
// 项目 15 只验证了"服务端是谁"（单向 TLS）；mTLS 把同一套机制镜像到
// 另一边：客户端也持叶子证书，服务端用 ClientAuth 策略验证它。
// 两个方向各一条链（本篇共用同一个 CA 体系，生产中通常分开）。
//
// 服务端配置的核心一行：ClientAuth: tls.RequireAndVerifyClientCert
// + ClientCAs: 客户端链的根 —— 没证书/假证书在握手里直接被拒。
//
// 三个实验（127.0.0.1:19596）：
//
//	第 1 步：无证书客户端 → 握手失败（bad certificate）
//	第 2 步：持有正确客户端证书 → 握手通过，服务端提取对端 CN 打印
//	第 3 步：错误 CA 签发的客户端证书 → 握手失败（unknown authority）
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

// CA：签发机构（与项目 15 同构）。
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

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

// signCert：签发一张由模板定制的证书。
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

// clientCertTemplate：客户端身份证书（ExtKeyUsage=ClientAuth）。
func clientCertTemplate(cn string, serial int64) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"hands-on-net"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, // 客户端身份
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		BasicConstraintsValid: true,
	}
}

// serverCertTemplate：服务端身份证书（ExtKeyUsage=ServerAuth）。
func serverCertTemplate(cn string, serial int64) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"hands-on-net"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		BasicConstraintsValid: true,
	}
}

// mTLSServerConfig：RequireAndVerifyClientCert 的服务端配置。
// ClientCAs 指定信任哪个 CA 签的客户端证书。
func mTLSServerConfig(srvCert tls.Certificate, clientRoot *x509.Certificate) *tls.Config {
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(clientRoot)
	return &tls.Config{
		Certificates: []tls.Certificate{srvCert},
		ClientAuth:   tls.RequireAndVerifyClientCert, // mTLS 的核心一行
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS12,
	}
}

func main() {
	ca, err := newCA("hands-on mTLS CA", 3000)
	if err != nil {
		fmt.Println(err)
		return
	}
	srvCert, err := ca.signCert(serverCertTemplate("mtls-server", 3001))
	if err != nil {
		fmt.Println(err)
		return
	}
	goodClient, err := ca.signCert(clientCertTemplate("alice", 3002))
	if err != nil {
		fmt.Println(err)
		return
	}

	// 服务端：验证客户端证书，提取对端 CN 打印
	srvCfg := mTLSServerConfig(srvCert, ca.Cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:19596", srvCfg)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				tlsConn := c.(*tls.Conn)
				if err := tlsConn.Handshake(); err != nil {
					fmt.Printf("  [server] 握手失败: %v\n", err)
					c.Close()
					return
				}
				certs := tlsConn.ConnectionState().PeerCertificates
				cn := "<none>"
				if len(certs) > 0 {
					cn = certs[0].Subject.CommonName
				}
				fmt.Fprintf(c, "hello %s (CN verified)\n", cn)
				c.Close()
			}(c)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	fmt.Println("== 第 1 步：无证书客户端 → 握手失败 ==")
	err = dialMTLS("127.0.0.1:19596", &tls.Config{
		RootCAs: rootsOf(ca), ServerName: "localhost",
	})
	reportErr(err)

	fmt.Println()
	fmt.Println("== 第 2 步：持有正确客户端证书 → 通过，对端 CN 被服务端验证 ==")
	conn, err := tls.Dial("tcp", "127.0.0.1:19596", &tls.Config{
		RootCAs:      rootsOf(ca),
		ServerName:   "localhost",
		Certificates: []tls.Certificate{goodClient},
	})
	if err != nil {
		fmt.Println("  dial:", err)
		return
	}
	buf := make([]byte, 128)
	n, _ := conn.Read(buf)
	fmt.Printf("  服务端回话: %q\n", string(buf[:n]))
	conn.Close()

	fmt.Println()
	fmt.Println("== 第 3 步：错误 CA 签发的客户端证书 → 握手失败 ==")
	rogueCA, err := newCA("rogue CA", 3999)
	if err != nil {
		fmt.Println(err)
		return
	}
	rogueClient, err := rogueCA.signCert(clientCertTemplate("mallory", 3998))
	if err != nil {
		fmt.Println(err)
		return
	}
	err = dialMTLS("127.0.0.1:19596", &tls.Config{
		RootCAs:      rootsOf(ca),
		ServerName:   "localhost",
		Certificates: []tls.Certificate{rogueClient},
	})
	reportErr(err)
}

// dialMTLS：客户端拨号 + 显式握手 + 读一次响应。
// TLS 1.3 的客户端握手在服务端拒绝证书时**本地仍然成功**——服务端发
// certificate_required 警报后关连接，客户端要到第一次读才收到错误。
// 这是 TLS 1.3 的真实行为（1.2 会在握手期报错），读一次才能确定结果。
func dialMTLS(addr string, cfg *tls.Config) error {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second},
		"tcp", addr, cfg)
	if err != nil {
		return err
	}
	if err := conn.Handshake(); err != nil {
		return err
	}
	buf := make([]byte, 128)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = conn.Read(buf)
	return err
}

func reportErr(err error) {
	if err == nil {
		fmt.Println("  竟然握手成功（不应发生）")
		return
	}
	fmt.Printf("  握手失败（正确行为）：%v\n", err)
}

// rootsOf：信任该 CA 的客户端根池。
func rootsOf(ca *CA) *x509.CertPool {
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	return roots
}
