package testinfra

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateCertBundle(t *testing.T) {
	bundle, err := GenerateCertBundle([]string{"localhost", "127.0.0.1"})
	require.NoError(t, err)

	require.NotEmpty(t, bundle.CACert)
	require.NotEmpty(t, bundle.CAKey)
	require.NotEmpty(t, bundle.ServerCert)
	require.NotEmpty(t, bundle.ServerKey)
	require.NotEmpty(t, bundle.ClientCert)
	require.NotEmpty(t, bundle.ClientKey)

	ca := parseCert(t, bundle.CACert)
	server := parseCert(t, bundle.ServerCert)
	client := parseCert(t, bundle.ClientCert)

	assert.True(t, ca.IsCA)
	assert.Equal(t, "pgmi-test-ca", ca.Subject.CommonName)

	assert.False(t, server.IsCA)
	assert.Contains(t, server.DNSNames, "localhost")
	require.Len(t, server.IPAddresses, 1)
	assert.Equal(t, "127.0.0.1", server.IPAddresses[0].String())
	assert.Contains(t, server.ExtKeyUsage, x509.ExtKeyUsageServerAuth)

	assert.False(t, client.IsCA)
	assert.Equal(t, "postgres", client.Subject.CommonName)
	assert.Contains(t, client.ExtKeyUsage, x509.ExtKeyUsageClientAuth)

	pool := x509.NewCertPool()
	pool.AddCert(ca)

	_, err = server.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	assert.NoError(t, err, "server cert should chain to CA")

	_, err = client.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	assert.NoError(t, err, "client cert should chain to CA")
}

func TestCertBundle_WriteToDir(t *testing.T) {
	bundle, err := GenerateCertBundle([]string{"localhost"})
	require.NoError(t, err)

	dir := t.TempDir()
	paths, err := bundle.WriteToDir(dir)
	require.NoError(t, err)

	for _, p := range []string{paths.CACert, paths.ServerCert, paths.ServerKey, paths.ClientCert, paths.ClientKey} {
		info, err := os.Stat(p)
		require.NoError(t, err, "file should exist: %s", p)
		if runtime.GOOS != "windows" {
			assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "file permissions for %s", p)
		}
	}
}

func TestGenerateCertBundle_InvalidCACantVerifyForeignCert(t *testing.T) {
	bundle1, err := GenerateCertBundle([]string{"localhost"})
	require.NoError(t, err)

	bundle2, err := GenerateCertBundle([]string{"localhost"})
	require.NoError(t, err)

	ca1 := parseCert(t, bundle1.CACert)
	client2 := parseCert(t, bundle2.ClientCert)

	pool := x509.NewCertPool()
	pool.AddCert(ca1)

	_, err = client2.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	assert.Error(t, err, "cert from different CA should not verify")
}

func parseCert(t *testing.T, pemData []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemData)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}
