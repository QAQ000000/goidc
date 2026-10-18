// Package csvexport encodes already authorized tabular data. Callers own the
// columns, permissions, filtering and row limits; this package never queries data.
package csvexport

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
)

// Encode returns UTF-8 CSV with a BOM for spreadsheet compatibility. Numeric
// values, including json.Number, are formatted directly without float conversion.
// Text cells with formula or control prefixes are explicitly marked as text.
func Encode(headers []string, rows [][]any) ([]byte, error) {
	var content bytes.Buffer
	content.WriteString("\xef\xbb\xbf")
	writer := csv.NewWriter(&content)
	if err := writer.Write(headers); err != nil {
		return nil, err
	}
	for _, row := range rows {
		cells := make([]string, len(row))
		for index, value := range row {
			cells[index] = cell(value)
		}
		if err := writer.Write(cells); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return content.Bytes(), writer.Error()
}

func cell(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		trimmed := strings.TrimLeft(text, " \t\r\n")
		if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) || strings.HasPrefix(text, "\t") || strings.HasPrefix(text, "\r") || strings.HasPrefix(text, "\n") {
			return "'" + text
		}
		return text
	}
	return fmt.Sprint(value)
}
