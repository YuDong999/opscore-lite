package utils

import (
	"context"
	"time"
)

// ContextWithTimeout returns a context with a timeout
func ContextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
