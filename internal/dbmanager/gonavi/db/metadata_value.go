package db

import (
	"fmt"
	"strings"
)

func parseMetadataBool(value interface{}) bool {
	switch val := value.(type) {
	case bool:
		return val
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	case string:
		text := strings.ToLower(strings.TrimSpace(val))
		return text == "t" || text == "true" || text == "1" || text == "y" || text == "yes" || text == "unique"
	default:
		text := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", value)))
		return text == "t" || text == "true" || text == "1" || text == "y" || text == "yes" || text == "unique"
	}
}

func parseMetadataInt(value interface{}) int {
	switch val := value.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(val), "%d", &n)
		return n
	default:
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(fmt.Sprintf("%v", value)), "%d", &n)
		return n
	}
}
