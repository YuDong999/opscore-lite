package db

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"strings"

	"opscore/internal/dbmanager/gonavi/connection"
	"opscore/internal/dbmanager/gonavi/tlsconfig"
)

const (
	sslModeDisable    = "disable"
	sslModePreferred  = "preferred"
	sslModeRequired   = "required"
	sslModeSkipVerify = "skip-verify"
)

func normalizeSSLModeValue(raw string) string {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "", sslModePreferred, "prefer":
		return sslModePreferred
	case sslModeRequired, "require", "on", "true", "mandatory", "strict":
		return sslModeRequired
	case sslModeSkipVerify, "insecure", "skipverify", "skip_verify", "insecure-skip-verify":
		return sslModeSkipVerify
	case sslModeDisable, "disabled", "off", "false", "none":
		return sslModeDisable
	default:
		return sslModePreferred
	}
}

func normalizedSSLMode(config connection.ConnectionConfig) string {
	if !config.UseSSL {
		return sslModeDisable
	}
	return normalizeSSLModeValue(config.SSLMode)
}

func shouldTrySSLPreferredFallback(config connection.ConnectionConfig) bool {
	return config.UseSSL && normalizeSSLModeValue(config.SSLMode) == sslModePreferred
}

func withSSLDisabled(config connection.ConnectionConfig) connection.ConnectionConfig {
	next := config
	next.UseSSL = false
	next.SSLMode = sslModeDisable
	return next
}

func resolveMySQLTLSMode(config connection.ConnectionConfig) string {
	switch normalizedSSLMode(config) {
	case sslModeDisable:
		return "false"
	case sslModeRequired:
		return "true"
	case sslModeSkipVerify:
		return "skip-verify"
	default:
		return "preferred"
	}
}

func hasTLSCertificatePaths(config connection.ConnectionConfig) bool {
	return strings.TrimSpace(config.SSLCAPath) != "" ||
		strings.TrimSpace(config.SSLCertPath) != "" ||
		strings.TrimSpace(config.SSLKeyPath) != ""
}

func mysqlTLSConfigName(config connection.ConnectionConfig) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		normalizedSSLMode(config),
		strings.TrimSpace(config.SSLCAPath),
		strings.TrimSpace(config.SSLCertPath),
		strings.TrimSpace(config.SSLKeyPath),
	}, "\x00")))
	return "gonavi-" + hex.EncodeToString(sum[:8])
}

func resolvePostgresSSLMode(config connection.ConnectionConfig) string {
	switch normalizedSSLMode(config) {
	case sslModeDisable:
		return "disable"
	case sslModeRequired:
		if strings.TrimSpace(config.SSLCAPath) != "" {
			return "verify-ca"
		}
		return "require"
	case sslModeSkipVerify:
		return "require"
	default:
		if strings.TrimSpace(config.SSLCAPath) != "" {
			return "verify-ca"
		}
		return "require"
	}
}

func resolveSQLServerTLSSettings(config connection.ConnectionConfig) (encrypt string, trustServerCertificate string) {
	switch normalizedSSLMode(config) {
	case sslModeDisable:
		return "disable", "true"
	case sslModeRequired:
		return "true", "false"
	case sslModeSkipVerify:
		return "true", "true"
	default:
		return "false", "true"
	}
}

func applyPostgresSSLPathParams(params interface{ Set(string, string) }, config connection.ConnectionConfig) {
	mode := normalizedSSLMode(config)
	if mode != sslModeDisable && mode != sslModeSkipVerify && strings.TrimSpace(config.SSLCAPath) != "" {
		params.Set("sslrootcert", strings.TrimSpace(config.SSLCAPath))
	}
	if mode != sslModeDisable && strings.TrimSpace(config.SSLCertPath) != "" {
		params.Set("sslcert", strings.TrimSpace(config.SSLCertPath))
	}
	if mode != sslModeDisable && strings.TrimSpace(config.SSLKeyPath) != "" {
		params.Set("sslkey", strings.TrimSpace(config.SSLKeyPath))
	}
}

func resolveGenericTLSConfig(config connection.ConnectionConfig) (*tls.Config, error) {
	switch normalizedSSLMode(config) {
	case sslModeDisable:
		return nil, nil
	case sslModeRequired:
		return tlsconfig.BuildClientConfig(tlsconfig.ClientConfigOptions{
			Enabled:  true,
			CAPath:   config.SSLCAPath,
			CertPath: config.SSLCertPath,
			KeyPath:  config.SSLKeyPath,
		})
	case sslModeSkipVerify:
		return tlsconfig.BuildClientConfig(tlsconfig.ClientConfigOptions{
			Enabled:            true,
			InsecureSkipVerify: true,
			CAPath:             config.SSLCAPath,
			CertPath:           config.SSLCertPath,
			KeyPath:            config.SSLKeyPath,
		})
	default:
		// Preferred: 先尝试 TLS（为提升兼容性默认跳过证书校验），失败时由调用方按需回退明文。
		return tlsconfig.BuildClientConfig(tlsconfig.ClientConfigOptions{
			Enabled:            true,
			InsecureSkipVerify: true,
			CAPath:             config.SSLCAPath,
			CertPath:           config.SSLCertPath,
			KeyPath:            config.SSLKeyPath,
		})
	}
}

func resolveMongoTLSSettings(config connection.ConnectionConfig) (enabled bool, insecure bool) {
	switch normalizedSSLMode(config) {
	case sslModeDisable:
		return false, false
	case sslModeRequired:
		return true, false
	case sslModeSkipVerify:
		return true, true
	default:
		return true, true
	}
}

func resolveTDengineNet(config connection.ConnectionConfig) string {
	if normalizedSSLMode(config) == sslModeDisable {
		return "ws"
	}
	return "wss"
}
