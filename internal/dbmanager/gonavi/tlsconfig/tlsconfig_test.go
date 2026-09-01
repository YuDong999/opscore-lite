package tlsconfig

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestBuildClientConfigLoadsCAAndClientCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, _ := writeSelfSignedCertificate(t, dir)

	cfg, err := BuildClientConfig(ClientConfigOptions{
		Enabled:  true,
		CAPath:   certPath,
		CertPath: certPath,
		KeyPath:  keyPath,
	})
	if err != nil {
		t.Fatalf("BuildClientConfig failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("config is nil")
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs is nil")
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates length = %d, want 1", len(cfg.Certificates))
	}
}

func TestBuildClientConfigLoadsDERCA(t *testing.T) {
	dir := t.TempDir()
	_, _, derBytes := writeSelfSignedCertificate(t, dir)
	derPath := filepath.Join(dir, "ca.cer")
	if err := os.WriteFile(derPath, derBytes, 0600); err != nil {
		t.Fatalf("write der cert: %v", err)
	}

	cfg, err := BuildClientConfig(ClientConfigOptions{
		Enabled: true,
		CAPath:  derPath,
	})
	if err != nil {
		t.Fatalf("BuildClientConfig failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("config is nil")
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs is nil")
	}
}

func TestBuildClientConfigRequiresClientCertificateAndKeyTogether(t *testing.T) {
	_, err := BuildClientConfig(ClientConfigOptions{
		Enabled:  true,
		CertPath: "client.pem",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildClientConfigErrorsUseEnglishWrappers(t *testing.T) {
	dir := t.TempDir()
	missingCAPath := filepath.Join(dir, "missing-ca.pem")
	invalidCAPath := filepath.Join(dir, "invalid-ca.pem")
	if err := os.WriteFile(invalidCAPath, []byte("not a certificate"), 0600); err != nil {
		t.Fatalf("write invalid CA: %v", err)
	}
	badCertPath := filepath.Join(dir, "bad-client.pem")
	badKeyPath := filepath.Join(dir, "bad-client.key")
	if err := os.WriteFile(badCertPath, []byte("not a cert"), 0600); err != nil {
		t.Fatalf("write bad cert: %v", err)
	}
	if err := os.WriteFile(badKeyPath, []byte("not a key"), 0600); err != nil {
		t.Fatalf("write bad key: %v", err)
	}

	cases := []struct {
		name       string
		options    ClientConfigOptions
		wantParts  []string
		wantUnwrap error
	}{
		{
			name: "missing CA",
			options: ClientConfigOptions{
				Enabled: true,
				CAPath:  missingCAPath,
			},
			wantParts:  []string{"failed to read TLS CA certificate", missingCAPath},
			wantUnwrap: os.ErrNotExist,
		},
		{
			name: "invalid CA",
			options: ClientConfigOptions{
				Enabled: true,
				CAPath:  invalidCAPath,
			},
			wantParts: []string{"TLS CA certificate is not a valid PEM/DER file", invalidCAPath},
		},
		{
			name: "missing client key",
			options: ClientConfigOptions{
				Enabled:  true,
				CertPath: badCertPath,
			},
			wantParts: []string{"TLS client certificate and private key must be configured together"},
		},
		{
			name: "invalid client pair",
			options: ClientConfigOptions{
				Enabled:  true,
				CertPath: badCertPath,
				KeyPath:  badKeyPath,
			},
			wantParts: []string{"failed to load TLS client certificate", "cert=" + badCertPath, "key=" + badKeyPath},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildClientConfig(tc.options)
			if err == nil {
				t.Fatal("expected error")
			}
			message := err.Error()
			if containsHan(message) {
				t.Fatalf("error wrapper must not contain Chinese text: %q", message)
			}
			for _, part := range tc.wantParts {
				if !strings.Contains(message, part) {
					t.Fatalf("error = %q, want to contain %q", message, part)
				}
			}
			if tc.wantUnwrap != nil && !errors.Is(err, tc.wantUnwrap) {
				t.Fatalf("error should unwrap to %v, got %v", tc.wantUnwrap, err)
			}
		})
	}
}

func containsHan(text string) bool {
	for _, r := range text {
		if unicode.In(r, unicode.Han) {
			return true
		}
	}
	return false
}

func writeSelfSignedCertificate(t *testing.T, dir string) (string, string, []byte) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "GoNavi Test",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPath := filepath.Join(dir, "cert.pem")
	certFile, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("create cert file: %v", err)
	}
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		_ = certFile.Close()
		t.Fatalf("write cert: %v", err)
	}
	if err := certFile.Close(); err != nil {
		t.Fatalf("close cert file: %v", err)
	}

	keyPath := filepath.Join(dir, "key.pem")
	keyFile, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("create key file: %v", err)
	}
	keyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	if err := pem.Encode(keyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes}); err != nil {
		_ = keyFile.Close()
		t.Fatalf("write key: %v", err)
	}
	if err := keyFile.Close(); err != nil {
		t.Fatalf("close key file: %v", err)
	}

	return certPath, keyPath, derBytes
}
