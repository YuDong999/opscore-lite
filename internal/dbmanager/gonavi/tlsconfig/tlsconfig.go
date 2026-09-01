package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

type ClientConfigOptions struct {
	Enabled            bool
	InsecureSkipVerify bool
	CAPath             string
	CertPath           string
	KeyPath            string
}

func BuildClientConfig(options ClientConfigOptions) (*tls.Config, error) {
	if !options.Enabled {
		return nil, nil
	}

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: options.InsecureSkipVerify,
	}

	caPath := strings.TrimSpace(options.CAPath)
	if caPath != "" {
		pemBytes, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read TLS CA certificate (%s): %w", caPath, err)
		}
		pool := x509.NewCertPool()
		if ok := pool.AppendCertsFromPEM(pemBytes); !ok {
			certs, err := x509.ParseCertificates(pemBytes)
			if err != nil || len(certs) == 0 {
				return nil, fmt.Errorf("TLS CA certificate is not a valid PEM/DER file: %s", caPath)
			}
			for _, cert := range certs {
				pool.AddCert(cert)
			}
		}
		cfg.RootCAs = pool
	}

	certPath := strings.TrimSpace(options.CertPath)
	keyPath := strings.TrimSpace(options.KeyPath)
	if (certPath == "") != (keyPath == "") {
		return nil, fmt.Errorf("TLS client certificate and private key must be configured together")
	}
	if certPath != "" {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS client certificate (cert=%s key=%s): %w", certPath, keyPath, err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}
