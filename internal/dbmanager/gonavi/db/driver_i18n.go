package db

import (
	"sync"

	"opscore/internal/dbmanager/gonavi/shared/i18n"
)

var (
	driverRuntimeTextMu        sync.RWMutex
	driverRuntimeTextLanguage  = i18n.LanguageZhCN
	driverRuntimeTextLocalizer *i18n.Localizer
)

func SetBackendLanguage(language i18n.Language) {
	normalized, ok := i18n.NormalizeLanguage(string(language))
	if !ok {
		return
	}

	driverRuntimeTextMu.Lock()
	defer driverRuntimeTextMu.Unlock()

	driverRuntimeTextLanguage = normalized
	if driverRuntimeTextLocalizer == nil {
		localizer, err := i18n.NewLocalizer(normalized)
		if err != nil {
			return
		}
		driverRuntimeTextLocalizer = localizer
		return
	}
	driverRuntimeTextLocalizer.SetLanguage(normalized)
}

func localizedDriverRuntimeText(key string, params map[string]any) string {
	driverRuntimeTextMu.RLock()
	if driverRuntimeTextLocalizer != nil {
		text := driverRuntimeTextLocalizer.T(key, params)
		driverRuntimeTextMu.RUnlock()
		return text
	}
	driverRuntimeTextMu.RUnlock()

	driverRuntimeTextMu.Lock()
	defer driverRuntimeTextMu.Unlock()

	if driverRuntimeTextLocalizer == nil {
		localizer, err := i18n.NewLocalizer(driverRuntimeTextLanguage)
		if err != nil {
			return key
		}
		driverRuntimeTextLocalizer = localizer
	}
	return driverRuntimeTextLocalizer.T(key, params)
}
