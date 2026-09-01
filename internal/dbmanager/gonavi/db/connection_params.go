package db

import (
	"net/url"
	"sort"
	"strings"

	"opscore/internal/dbmanager/gonavi/connection"
)

func parseConnectionURI(raw string, allowedSchemes ...string) (*url.URL, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, false
	}
	if strings.HasPrefix(strings.ToLower(text), "jdbc:") {
		text = strings.TrimSpace(text[len("jdbc:"):])
	}
	parsed, err := url.Parse(text)
	if err != nil {
		return nil, false
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	for _, allowed := range allowedSchemes {
		if scheme == strings.ToLower(strings.TrimSpace(allowed)) {
			return parsed, true
		}
	}
	return nil, false
}

func connectionParamsFromText(raw string) url.Values {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	if queryIndex := strings.Index(text, "?"); queryIndex >= 0 {
		text = text[queryIndex+1:]
	}
	if hashIndex := strings.Index(text, "#"); hashIndex >= 0 {
		text = text[:hashIndex]
	}
	text = strings.TrimLeft(strings.TrimSpace(text), "?&")
	if text == "" {
		return nil
	}
	values, err := url.ParseQuery(text)
	if err != nil {
		return nil
	}
	return values
}

func connectionParamsFromURI(raw string, allowedSchemes ...string) url.Values {
	parsed, ok := parseConnectionURI(raw, allowedSchemes...)
	if !ok {
		return nil
	}
	return parsed.Query()
}

func mergeConnectionParamValues(params url.Values, values url.Values) {
	if len(values) == 0 {
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, value := range values[key] {
			params.Set(key, value)
		}
	}
}

func normalizeConnectionParamName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}

func newConnectionParamNameMap(names ...string) map[string]string {
	result := make(map[string]string, len(names))
	for _, name := range names {
		addConnectionParamName(result, name)
	}
	return result
}

func addConnectionParamName(names map[string]string, canonical string) {
	key := normalizeConnectionParamName(canonical)
	if key == "" {
		return
	}
	names[key] = canonical
}

func addConnectionParamAlias(names map[string]string, alias string, canonical string) {
	key := normalizeConnectionParamName(alias)
	if key == "" {
		return
	}
	if existing, ok := names[normalizeConnectionParamName(canonical)]; ok {
		canonical = existing
	}
	names[key] = canonical
}

func mergeConnectionParamValuesWithAllowlist(params url.Values, values url.Values, canonicalNames map[string]string) {
	if len(values) == 0 || len(canonicalNames) == 0 {
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		canonical, ok := canonicalNames[normalizeConnectionParamName(key)]
		if !ok {
			continue
		}
		for _, value := range values[key] {
			params.Set(canonical, value)
		}
	}
}

func mergeConnectionParamsFromConfig(params url.Values, config connection.ConnectionConfig, allowedSchemes ...string) {
	mergeConnectionParamValues(params, connectionParamsFromURI(config.URI, allowedSchemes...))
	mergeConnectionParamValues(params, connectionParamsFromText(config.ConnectionParams))
}

func mergeConnectionParamsFromConfigWithAllowlist(params url.Values, config connection.ConnectionConfig, canonicalNames map[string]string, allowedSchemes ...string) {
	mergeConnectionParamValuesWithAllowlist(params, connectionParamsFromURI(config.URI, allowedSchemes...), canonicalNames)
	mergeConnectionParamValuesWithAllowlist(params, connectionParamsFromText(config.ConnectionParams), canonicalNames)
}

func mergeConnectionParamsIntoRawURI(raw string, connectionParams string, allowedSchemes ...string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return text
	}
	parsed, ok := parseConnectionURI(text, allowedSchemes...)
	if !ok {
		return text
	}
	params := parsed.Query()
	mergeConnectionParamValues(params, connectionParamsFromText(connectionParams))
	parsed.RawQuery = params.Encode()
	return parsed.String()
}

func isSafeConnectionParamKey(key string) bool {
	text := strings.TrimSpace(key)
	if text == "" {
		return false
	}
	for _, r := range text {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '_', '-', '.', ' ':
			continue
		default:
			return false
		}
	}
	return true
}
