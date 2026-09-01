package proxy

import (
	"sync"

	"opscore/internal/dbmanager/gonavi/shared/i18n"
)

var (
	proxyBackendTextMu        sync.RWMutex
	proxyBackendTextLanguage  = i18n.LanguageZhCN
	proxyBackendTextLocalizer *i18n.Localizer
)

type proxyLocalizedError struct {
	message string
	cause   error
}

func (e proxyLocalizedError) Error() string {
	return e.message
}

func (e proxyLocalizedError) Unwrap() error {
	return e.cause
}

func SetBackendLanguage(language i18n.Language) {
	normalized, ok := i18n.NormalizeLanguage(string(language))
	if !ok {
		return
	}

	proxyBackendTextMu.Lock()
	defer proxyBackendTextMu.Unlock()

	proxyBackendTextLanguage = normalized
	if proxyBackendTextLocalizer == nil {
		localizer, err := i18n.NewLocalizer(normalized)
		if err != nil {
			return
		}
		proxyBackendTextLocalizer = localizer
		return
	}
	proxyBackendTextLocalizer.SetLanguage(normalized)
}

func localizedProxyBackendText(key string, params map[string]any) string {
	proxyBackendTextMu.RLock()
	if proxyBackendTextLocalizer != nil {
		text := proxyBackendTextLocalizer.T(key, params)
		proxyBackendTextMu.RUnlock()
		return text
	}
	proxyBackendTextMu.RUnlock()

	proxyBackendTextMu.Lock()
	defer proxyBackendTextMu.Unlock()

	if proxyBackendTextLocalizer == nil {
		localizer, err := i18n.NewLocalizer(proxyBackendTextLanguage)
		if err != nil {
			return key
		}
		proxyBackendTextLocalizer = localizer
	}
	return proxyBackendTextLocalizer.T(key, params)
}

func proxyTextError(key string, params map[string]any) error {
	return proxyLocalizedError{message: localizedProxyBackendText(key, params)}
}

func proxyWrapError(key string, params map[string]any, cause error) error {
	return proxyLocalizedError{
		message: localizedProxyBackendText(key, params),
		cause:   cause,
	}
}
