package db

import (
	"time"

	"opscore/internal/dbmanager/gonavi/connection"
)

const defaultConnectTimeoutSeconds = 30

func getConnectTimeoutSeconds(config connection.ConnectionConfig) int {
	timeoutSeconds := config.Timeout
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultConnectTimeoutSeconds
	}
	return timeoutSeconds
}

func getConnectTimeout(config connection.ConnectionConfig) time.Duration {
	return time.Duration(getConnectTimeoutSeconds(config)) * time.Second
}

