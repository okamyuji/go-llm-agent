package httpapi

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// TLSConfig TLS / mTLS の設定
type TLSConfig struct {
	Enabled      bool
	CertFile     string
	KeyFile      string
	ClientCAFile string
	MinVersion   string
}

// BuildTLSConfig TLSConfig を *tls.Config に変換する
// Enabled=false のときは nil を返し、呼び出し側は HTTP モードで起動する
// ClientCAFile が指定されているときは mTLS (RequireAndVerifyClientCert) を強制する
func BuildTLSConfig(c TLSConfig) (*tls.Config, error) {
	if !c.Enabled {
		return nil, nil
	}
	if c.CertFile == "" || c.KeyFile == "" {
		return nil, errors.New("httpapi: tls.enabled=true requires cert_file and key_file")
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("httpapi tls keypair: %w", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   resolveMinVersion(c.MinVersion),
	}
	if c.ClientCAFile != "" {
		caBytes, err := os.ReadFile(c.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("httpapi tls client ca read: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, errors.New("httpapi: client_ca_file did not contain any valid certificate")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// resolveMinVersion 文字列から TLS バージョン定数に解決する
// TLS 1.0 / 1.1 は IETF RFC 8996 で deprecated のため受け付けない。
// 空または未知の値はセキュアな既定として TLS 1.2 に落とす
func resolveMinVersion(v string) uint16 {
	switch v {
	case "1.2":
		return tls.VersionTLS12
	case "1.3":
		return tls.VersionTLS13
	default:
		return tls.VersionTLS12
	}
}
