// 项目 14：TLS 握手观察——自签服务、证书链与 ConnectionState 全景。
//
// crypto/tls 把握手包进 Dial 里，本篇把它拆开看：
//
//	第 1 步：自签证书的 TLS 服务 + 信任池客户端——握手成功后打印
//	        ConnectionState 的版本/密码套件/对端证书链（SAN、有效期）
//	第 2 步：默认信任池的客户端连自签服务——x509.UnknownAuthorityError
//	        （自签证书不在系统根存储里，被拒是正确行为）
//	第 3 步：InsecureSkipVerify 跳过验证能连上——危险开关，仅调试用
//	第 4 步：openssl s_client 从外部观察同一条链（命令与输出实录）
//
// 运行方式：go run .（127.0.0.1:19593）；断言见 go test -race。
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
	"os/exec"
	"strings"
	"time"
)

const addr = "127.0.0.1:19593"

// genSelfSigned：自签证书——SAN 必须含 127.0.0.1 与 localhost，否则主机名校验失败。
func genSelfSigned() tls.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost", Organization: []string{"hands-on-net"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// trustedClient：把服务端叶子证书加进根池的客户端（信任自签）。
func trustedClient(cert *x509.Certificate) *tls.Config {
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &tls.Config{RootCAs: roots, ServerName: "localhost"}
}

// describe：把 ConnectionState 翻译成人话（握手结果的全景打印）。
func describe(cs tls.ConnectionState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  握手完成=%v  协议版本=%s\n", cs.HandshakeComplete, tls.VersionName(cs.Version))
	fmt.Fprintf(&b, "  密码套件=%s\n", tls.CipherSuiteName(cs.CipherSuite))
	for i, pc := range cs.PeerCertificates {
		fmt.Fprintf(&b, "  证书链[%d]: CN=%q  有效期 %s ~ %s\n",
			i, pc.Subject.CommonName,
			pc.NotBefore.Format("2006-01-02"), pc.NotAfter.Format("2006-01-02"))
		if i == 0 {
			fmt.Fprintf(&b, "  叶子 SAN: DNS=%v IP=%v\n", pc.DNSNames, pc.IPAddresses)
		}
	}
	return b.String()
}

func main() {
	cert := genSelfSigned()
	srvCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := tls.Listen("tcp", addr, srvCfg)
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
				fmt.Fprintf(c, "hello over TLS\n")
				c.Close()
			}(c)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	fmt.Println("== 第 1 步：信任池客户端——握手成功，ConnectionState 全景 ==")
	conn, err := tls.Dial("tcp", addr, trustedClient(cert.Leaf))
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	cs := conn.ConnectionState()
	fmt.Print(describe(cs))
	conn.Close()

	fmt.Println()
	fmt.Println("== 第 2 步：默认信任池客户端——自签被拒（正确行为）==")
	_, err = tls.Dial("tcp", addr, &tls.Config{}) // 系统根存储里没有这张自签证书
	if err != nil {
		fmt.Printf("  错误类型：%T\n  错误内容：%v\n", err, err)
	}

	fmt.Println()
	fmt.Println("== 第 3 步：InsecureSkipVerify——能连上，但这是危险开关 ==")
	insecure, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	fmt.Printf("  跳过验证握手成功=%v——只用于调试，生产禁用\n",
		insecure.ConnectionState().HandshakeComplete)
	insecure.Close()

	fmt.Println()
	fmt.Println("== 第 4 步：openssl s_client 外部观察 ==")
	out, err := exec.Command("openssl", "s_client", "-connect", addr,
		"-CAfile", "/dev/null", "-brief").CombinedOutput()
	if err != nil || len(out) == 0 {
		// -CAfile /dev/null 在部分 openssl 版本行为不同，退回不带 -brief
		out2, err2 := exec.Command("openssl", "s_client", "-connect", addr).CombinedOutput()
		if err2 != nil {
			fmt.Println("openssl 不可用：", err2)
			return
		}
		printLines(string(out2), "Protocol", "Cipher", "Verification")
		return
	}
	printLines(string(out), "Protocol", "Cipher", "Verification")
}

// printLines：从 openssl 输出里抽取含关键词的行。
func printLines(out string, keys ...string) {
	for _, line := range strings.Split(out, "\n") {
		for _, k := range keys {
			if strings.Contains(line, k) {
				fmt.Println("  " + strings.TrimSpace(line))
			}
		}
	}
}
