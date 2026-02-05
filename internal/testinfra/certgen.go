package testinfra

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

type CertBundle struct {
	CACert, CAKey         []byte
	ServerCert, ServerKey []byte
	ClientCert, ClientKey []byte
}

type CertPaths struct {
	CACert     string
	ServerCert string
	ServerKey  string
	ClientCert string
	ClientKey  string
}

func GenerateCertBundle(hosts []string) (*CertBundle, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "pgmi-test-ca"},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(1 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate server key: %w", err)
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "pgmi-test-server"},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(1 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			serverTemplate.IPAddresses = append(serverTemplate.IPAddresses, ip)
		} else {
			serverTemplate.DNSNames = append(serverTemplate.DNSNames, h)
		}
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("create server certificate: %w", err)
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate client key: %w", err)
	}

	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "postgres"},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(1 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("create client certificate: %w", err)
	}

	caKeyPEM, err := encodeKeyPEM(caKey)
	if err != nil {
		return nil, fmt.Errorf("encode CA key: %w", err)
	}
	serverKeyPEM, err := encodeKeyPEM(serverKey)
	if err != nil {
		return nil, fmt.Errorf("encode server key: %w", err)
	}
	clientKeyPEM, err := encodeKeyPEM(clientKey)
	if err != nil {
		return nil, fmt.Errorf("encode client key: %w", err)
	}

	bundle := &CertBundle{
		CACert:     encodeCertPEM(caCertDER),
		CAKey:      caKeyPEM,
		ServerCert: encodeCertPEM(serverCertDER),
		ServerKey:  serverKeyPEM,
		ClientCert: encodeCertPEM(clientCertDER),
		ClientKey:  clientKeyPEM,
	}

	return bundle, nil
}

func (b *CertBundle) WriteToDir(dir string) (*CertPaths, error) {
	paths := &CertPaths{
		CACert:     filepath.Join(dir, "ca.crt"),
		ServerCert: filepath.Join(dir, "server.crt"),
		ServerKey:  filepath.Join(dir, "server.key"),
		ClientCert: filepath.Join(dir, "client.crt"),
		ClientKey:  filepath.Join(dir, "client.key"),
	}

	files := map[string][]byte{
		paths.CACert:     b.CACert,
		paths.ServerCert: b.ServerCert,
		paths.ServerKey:  b.ServerKey,
		paths.ClientCert: b.ClientCert,
		paths.ClientKey:  b.ClientKey,
	}

	for path, data := range files {
		if err := os.WriteFile(path, data, 0600); err != nil {
			return nil, fmt.Errorf("write %s: %w", filepath.Base(path), err)
		}
	}

	return paths, nil
}

func encodeCertPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func encodeKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}
