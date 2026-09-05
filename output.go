package mcpcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Column struct{ Header, Path string }

// Render emits the complete MCP result in JSON mode. Human mode presents
// structured content where available; applications supply useful column paths.
func Render(w io.Writer, result *mcp.CallToolResult, format string, columns []Column) error {
	if result == nil {
		return errors.New("MCP returned no result")
	}
	if format == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	if format != "table" {
		return errors.New("output must be table or json")
	}
	if result.StructuredContent == nil {
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				if _, err := fmt.Fprintln(w, safe(text.Text)); err != nil {
					return err
				}
			} else {
				raw, err := json.Marshal(content)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintln(w, safe(string(raw))); err != nil {
					return err
				}
			}
		}
		return nil
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	value, err := decodeValue(raw)
	if err != nil {
		return err
	}
	if object, ok := value.(map[string]any); ok {
		if data, exists := object["data"]; exists {
			value = data
		}
	}
	next := ""
	if object, ok := value.(map[string]any); ok {
		if items, exists := object["items"]; exists {
			value = items
			next, _ = object["nextCursor"].(string)
		}
	}
	rows, ok := value.([]any)
	if !ok {
		rows = []any{value}
	}
	if len(rows) == 0 {
		if _, err := fmt.Fprintln(w, "No results."); err != nil {
			return err
		}
		if next != "" {
			_, err := fmt.Fprintf(w, "Next cursor: %s\n", cell(next))
			return err
		}
		return nil
	}
	if len(columns) == 0 {
		if object, ok := rows[0].(map[string]any); ok {
			keys := make([]string, 0, len(object))
			for key, value := range object {
				switch value.(type) {
				case nil, string, bool, json.Number:
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				columns = append(columns, Column{Header: key, Path: key})
			}
		}
	}
	if len(columns) == 0 {
		pretty, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintln(w, safe(string(pretty))); err != nil {
			return err
		}
		if next != "" {
			_, err = fmt.Fprintf(w, "Next cursor: %s\n", cell(next))
			return err
		}
		return nil
	}
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	headers := make([]string, len(columns))
	for i, column := range columns {
		headers[i] = cell(column.Header)
	}
	if _, err := fmt.Fprintln(table, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, column := range columns {
			value := atPath(row, column.Path)
			if value == nil {
				cells[i] = "-"
				continue
			}
			if text, ok := value.(string); ok {
				cells[i] = cell(text)
			} else {
				raw, err := json.Marshal(value)
				if err != nil {
					return err
				}
				cells[i] = cell(string(raw))
			}
		}
		if _, err := fmt.Fprintln(table, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if next != "" {
		_, err := fmt.Fprintf(w, "Next cursor: %s\n", cell(next))
		return err
	}
	return nil
}
func atPath(value any, path string) any {
	if path == "" {
		return value
	}
	for _, part := range strings.Split(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[part]
	}
	return value
}
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || r == '\x1b' {
			return '�'
		}
		return r
	}, s)
}
func cell(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(safe(s))
}
