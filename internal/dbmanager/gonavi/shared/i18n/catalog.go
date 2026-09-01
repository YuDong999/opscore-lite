package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

type Language string

const (
	LanguageZhCN Language = "zh-CN"
	LanguageZhTW Language = "zh-TW"
	LanguageEnUS Language = "en-US"
	LanguageJaJP Language = "ja-JP"
	LanguageDeDE Language = "de-DE"
	LanguageRuRU Language = "ru-RU"
)

const PreferenceSystem = "system"

var supportedLanguages = map[Language]struct{}{
	LanguageZhCN: {},
	LanguageZhTW: {},
	LanguageEnUS: {},
	LanguageJaJP: {},
	LanguageDeDE: {},
	LanguageRuRU: {},
}

var supportedLanguageOrder = []Language{
	LanguageZhCN,
	LanguageZhTW,
	LanguageEnUS,
	LanguageJaJP,
	LanguageDeDE,
	LanguageRuRU,
}

//go:embed zh-CN.json zh-TW.json en-US.json ja-JP.json de-DE.json ru-RU.json
var catalogFS embed.FS

type Catalog map[string]string

func NormalizeLanguage(value string) (Language, bool) {
	normalized := strings.TrimSpace(strings.ReplaceAll(value, "_", "-"))
	if normalized == "" {
		return "", false
	}
	lower := strings.ToLower(normalized)
	switch {
	case lower == "zh-tw" || lower == "zh-hk" || lower == "zh-mo":
		return LanguageZhTW, true
	case lower == "zh-cn" || lower == "zh-sg" || lower == "zh":
		return LanguageZhCN, true
	case lower == "en-us" || strings.HasPrefix(lower, "en-"):
		return LanguageEnUS, true
	case lower == "ja" || strings.HasPrefix(lower, "ja-"):
		return LanguageJaJP, true
	case lower == "de" || strings.HasPrefix(lower, "de-"):
		return LanguageDeDE, true
	case lower == "ru" || strings.HasPrefix(lower, "ru-"):
		return LanguageRuRU, true
	default:
		lang := Language(normalized)
		_, ok := supportedLanguages[lang]
		return lang, ok
	}
}

func ResolveLanguage(preference string, systemLanguages []string) Language {
	if lang, ok := NormalizeLanguage(preference); ok {
		return lang
	}
	for _, systemLanguage := range systemLanguages {
		if lang, ok := NormalizeLanguage(systemLanguage); ok {
			return lang
		}
	}
	return LanguageEnUS
}

func SupportedLanguages() []Language {
	languages := make([]Language, len(supportedLanguageOrder))
	copy(languages, supportedLanguageOrder)
	return languages
}

func LoadCatalogs() (map[Language]Catalog, error) {
	result := make(map[Language]Catalog, len(supportedLanguageOrder))
	for _, lang := range supportedLanguageOrder {
		payload, err := catalogFS.ReadFile(string(lang) + ".json")
		if err != nil {
			return nil, err
		}
		var catalog Catalog
		if err := json.Unmarshal(payload, &catalog); err != nil {
			return nil, err
		}
		result[lang] = catalog
	}
	return result, nil
}

type Localizer struct {
	language Language
	catalogs map[Language]Catalog
}

func NewLocalizer(language Language) (*Localizer, error) {
	catalogs, err := LoadCatalogs()
	if err != nil {
		return nil, err
	}
	if _, ok := supportedLanguages[language]; !ok {
		language = LanguageEnUS
	}
	return &Localizer{language: language, catalogs: catalogs}, nil
}

func (l *Localizer) SetLanguage(language Language) {
	if _, ok := supportedLanguages[language]; ok {
		l.language = language
	}
}

func (l *Localizer) Language() Language {
	if l == nil {
		return LanguageEnUS
	}
	return l.language
}

func (l *Localizer) T(key string, params map[string]any) string {
	if l == nil {
		return key
	}
	template := ""
	if catalog, ok := l.catalogs[l.language]; ok {
		template = catalog[key]
	}
	if template == "" && l.language != LanguageEnUS {
		template = l.catalogs[LanguageEnUS][key]
	}
	if template == "" {
		return key
	}
	for name, value := range params {
		template = strings.ReplaceAll(template, "{{"+name+"}}", toString(value))
	}
	return template
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}
