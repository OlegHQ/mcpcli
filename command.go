// Package mcpcli adapts MCP tool schemas to Cobra commands. Applications own
// authentication, discovery, transport, and domain-specific presentation.
package mcpcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

type InvokeFunc func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error)
type Binding struct {
	Path        []string
	Positionals []string
	Flags       map[string]string
	Columns     []Column
}
type Options struct {
	Name, Description, Version string
	Tools                      []*mcp.Tool
	Invoke                     InvokeFunc
	Bindings                   map[string]Binding
	// MaxInputBytes bounds --input files/stdin; zero uses 20,000,000 bytes.
	MaxInputBytes int64
	OutputFormat  func(*cobra.Command) string
}

var ErrTool = errors.New("MCP tool failed")

func NewCommand(opts Options) (*cobra.Command, error) {
	if !validName(opts.Name) {
		return nil, errors.New("command name is required")
	}
	root := &cobra.Command{Use: opts.Name, Short: opts.Description, Version: opts.Version, SilenceErrors: true, SilenceUsage: true}
	if err := AddCommands(root, opts); err != nil {
		return nil, err
	}
	return root, nil
}
func AddCommands(root *cobra.Command, opts Options) error {
	if root == nil || opts.Invoke == nil {
		return errors.New("root and Invoke are required")
	}
	if opts.MaxInputBytes == 0 {
		opts.MaxInputBytes = 20_000_000
	}
	if opts.MaxInputBytes < 1 {
		return errors.New("MaxInputBytes must be positive")
	}
	if root.PersistentFlags().Lookup("output") == nil && root.InheritedFlags().Lookup("output") == nil {
		root.PersistentFlags().String("output", "table", "Output format: table or json")
	}
	names := map[string]bool{}
	for _, tool := range opts.Tools {
		if tool == nil || tool.Name == "" || names[tool.Name] {
			return errors.New("tools require unique nonempty names")
		}
		names[tool.Name] = true
		binding := opts.Bindings[tool.Name]
		if len(binding.Path) == 0 {
			binding.Path = []string{kebab(tool.Name)}
		}
		parent := root
		for i, part := range binding.Path {
			if !validName(part) {
				return fmt.Errorf("invalid command path for %s", tool.Name)
			}
			var existing *cobra.Command
			for _, child := range parent.Commands() {
				if slices.Contains(child.Aliases, part) {
					return fmt.Errorf("command path %s collides with an existing alias", part)
				}
				if child.Name() == part {
					existing = child
					break
				}
			}
			if i < len(binding.Path)-1 {
				if existing == nil {
					existing = &cobra.Command{Use: part, Short: part + " commands"}
					parent.AddCommand(existing)
				}
				if existing.Run != nil || existing.RunE != nil {
					return fmt.Errorf("command path collision at %s", part)
				}
				parent = existing
				continue
			}
			if existing != nil {
				return fmt.Errorf("command path collision at %s", part)
			}
			command, err := toolCommand(tool, binding, opts, parent)
			if err != nil {
				return fmt.Errorf("tool %s: %w", tool.Name, err)
			}
			command.Use = part
			for _, position := range binding.Positionals {
				command.Use += " [" + position + "]"
			}
			parent.AddCommand(command)
		}
	}
	return nil
}

func toolCommand(tool *mcp.Tool, binding Binding, opts Options, parent *cobra.Command) (*cobra.Command, error) {
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("decode input schema: %w", err)
	}
	if err := checkProperties(&schema); err != nil {
		return nil, err
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve input schema: %w", err)
	}
	cmd := &cobra.Command{Short: safe(tool.Description), Args: cobra.MaximumNArgs(len(binding.Positionals)), SilenceUsage: true}
	cmd.ValidArgsFunction = cobra.NoFileCompletions
	if ancestorFlag(parent, "input") {
		return nil, errors.New("input flag conflicts with an ancestor flag")
	}
	cmd.Flags().String("input", "", "Read the complete JSON argument object from a file, or - for stdin; cannot combine with argument flags")
	fields, err := addFields(cmd, parent, schema.Properties, binding)
	if err != nil {
		return nil, err
	}
	byPath := map[string]*field{}
	for _, f := range fields {
		if f.mode == "" {
			byPath[f.path] = f
		}
	}
	for _, path := range binding.Positionals {
		if byPath[path] == nil {
			return nil, fmt.Errorf("unknown positional property %s", path)
		}
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		format := ""
		if opts.OutputFormat != nil {
			format = opts.OutputFormat(cmd)
		} else {
			var err error
			format, err = cmd.Flags().GetString("output")
			if err != nil {
				return err
			}
		}
		if format != "json" && format != "table" {
			return fmt.Errorf("output must be table or json")
		}
		values := map[string]any{}
		input, _ := cmd.Flags().GetString("input")
		if cmd.Flags().Changed("input") {
			for _, f := range fields {
				if cmd.Flags().Changed(f.name) {
					return errors.New("--input cannot be combined with argument flags")
				}
			}
			if len(args) > 0 {
				return errors.New("--input cannot be combined with positional arguments")
			}
			var reader io.Reader = cmd.InOrStdin()
			if input != "-" {
				file, err := os.Open(input)
				if err != nil {
					return fmt.Errorf("open input file: %w", err)
				}
				defer file.Close()
				reader = file
			}
			raw, err := io.ReadAll(io.LimitReader(reader, opts.MaxInputBytes+1))
			if err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			if int64(len(raw)) > opts.MaxInputBytes {
				return errors.New("input exceeds byte limit")
			}
			decoded, err := decodeValue(raw)
			if err != nil {
				return fmt.Errorf("invalid JSON input: %w", err)
			}
			var ok bool
			values, ok = decoded.(map[string]any)
			if !ok || values == nil {
				return errors.New("input must be a JSON object")
			}
		} else {
			node := &argumentNode{}
			for _, f := range fields {
				if !cmd.Flags().Changed(f.name) {
					continue
				}
				if err := f.assign(node); err != nil {
					return fmt.Errorf("--%s: %w", f.name, err)
				}
			}
			for i, value := range args {
				f := byPath[binding.Positionals[i]]
				if f.indexCount() > 0 {
					return fmt.Errorf("indexed properties cannot be positional: %s", f.path)
				}
				if cmd.Flags().Changed(f.name) {
					return fmt.Errorf("duplicate value for %s", f.path)
				}
				entry, err := f.parse(value)
				if err != nil {
					return err
				}
				if err := node.assign(entry.path, entry.value, entry.append); err != nil {
					return err
				}
			}
			assembled, err := node.materialize()
			if err != nil {
				return err
			}
			if assembled != nil {
				values = assembled.(map[string]any)
			}
		}

		validation, err := validationValue(values)
		if err != nil {
			return err
		}
		if err := resolved.Validate(validation); err != nil {
			return fmt.Errorf("invalid arguments: %w", err)
		}
		raw, err := json.Marshal(values)
		if err != nil {
			return err
		}
		if int64(len(raw)) > opts.MaxInputBytes {
			return errors.New("input exceeds byte limit")
		}
		result, err := opts.Invoke(cmd.Context(), tool.Name, raw)
		if err != nil {
			return err
		}
		if result == nil {
			return errors.New("MCP returned no result")
		}
		if result.IsError {
			if err := Render(cmd.ErrOrStderr(), result, format, binding.Columns); err != nil {
				return err
			}
			return ErrTool
		}
		return Render(cmd.OutOrStdout(), result, format, binding.Columns)
	}
	return cmd, nil
}
func schemaType(s *jsonschema.Schema) string {
	if s == nil {
		return "json"
	}
	if s.Type != "" {
		return s.Type
	}
	result := ""
	for _, t := range s.Types {
		if t == "null" {
			continue
		}
		if result != "" {
			return "json"
		}
		result = t
	}
	if result != "" {
		return result
	}
	return "json"
}
func parseValue(raw string, s *jsonschema.Schema) (any, error) {
	if schemaType(s) == "string" {
		return raw, nil
	}
	value, err := decodeValue([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("expected %s value", schemaType(s))
	}
	return value, nil
}
func decodeValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("expected exactly one JSON value")
	}
	return value, nil
}
func kebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r == '_' {
			b.WriteByte('-')
			continue
		}
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('-')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// The schema library's type checks use native Go number types. Keep the
// json.Number values for encoding, and validate a numeric copy without rounding
// integers through float64.
func validationValue(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		text := value.String()
		if !strings.ContainsAny(text, ".eE") {
			if number, err := strconv.ParseInt(text, 10, 64); err == nil {
				return number, nil
			}
			if number, err := strconv.ParseUint(text, 10, 64); err == nil {
				return number, nil
			}
			return nil, errors.New("integer exceeds supported 64-bit validation range")
		}
		number, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, errors.New("number exceeds supported validation range")
		}
		return number, nil
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			converted, err := validationValue(item)
			if err != nil {
				return nil, err
			}
			result[key] = converted
		}
		return result, nil
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			converted, err := validationValue(item)
			if err != nil {
				return nil, err
			}
			result[index] = converted
		}
		return result, nil
	default:
		return value, nil
	}
}

var cliName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func validName(name string) bool { return cliName.MatchString(name) }
func ancestorFlag(parent *cobra.Command, name string) bool {
	for command := parent; command != nil; command = command.Parent() {
		if command.Flags().Lookup(name) != nil || command.PersistentFlags().Lookup(name) != nil {
			return true
		}
	}
	return false
}
func checkProperties(schema *jsonschema.Schema) error {
	if schema == nil {
		return nil
	}
	for name, property := range schema.Properties {
		if property == nil {
			return fmt.Errorf("property %q has a null schema", name)
		}
		if err := checkProperties(property); err != nil {
			return err
		}
	}
	for _, definition := range schema.Defs {
		if err := checkProperties(definition); err != nil {
			return err
		}
	}
	for _, definition := range schema.Definitions {
		if err := checkProperties(definition); err != nil {
			return err
		}
	}
	for _, list := range [][]*jsonschema.Schema{schema.AnyOf, schema.OneOf, schema.AllOf, schema.PrefixItems} {
		for _, item := range list {
			if err := checkProperties(item); err != nil {
				return err
			}
		}
	}
	return checkProperties(schema.Items)
}
