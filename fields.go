package mcpcli

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/spf13/cobra"
)

// Indexed flags keep ordinary Cobra parsing and help. A schema path such as
// issues.[].labels produces --issues-labels 0=bug, repeated to append labels.
type field struct {
	path, name, mode string
	schema           *jsonschema.Schema
	entries          []fieldEntry
}

type fieldEntry struct {
	path   []argumentPart
	value  any
	append bool
}

type argumentPart struct {
	key   string
	index int
	array bool
}

func (f *field) String() string { return "" }
func (f *field) Type() string {
	if f.indexCount() > 0 {
		if f.mode != "" {
			return "index"
		}
		return "index=value"
	}
	if f.mode != "" {
		return "boolean"
	}
	if scalarArray(f.schema) {
		return schemaType(f.schema.Items)
	}
	return schemaType(f.schema)
}

func (f *field) Set(raw string) error {
	entry, err := f.parse(raw)
	if err != nil {
		return err
	}
	f.entries = append(f.entries, entry)
	return nil
}

func (f *field) indexCount() int { return strings.Count(f.path, ".[]") }

func (f *field) parse(raw string) (fieldEntry, error) {
	var entry fieldEntry
	var indices []string
	if count := f.indexCount(); count > 0 {
		indexText := raw
		if f.mode == "" {
			var found bool
			indexText, raw, found = strings.Cut(raw, "=")
			if !found {
				return entry, errors.New("expected INDEX=VALUE (nested indices use dots, for example 0.1=value)")
			}
		}
		indices = strings.Split(indexText, ".")
		if len(indices) != count {
			return entry, fmt.Errorf("expected %d zero-based indices separated by dots", count)
		}
	}
	for _, part := range strings.Split(f.path, ".") {
		if part != "[]" {
			entry.path = append(entry.path, argumentPart{key: part})
			continue
		}
		text := indices[0]
		indices = indices[1:]
		index, err := strconv.ParseUint(text, 10, 31)
		if err != nil || strconv.FormatUint(index, 10) != text {
			return entry, errors.New("indices must be non-negative decimal integers without leading zeroes")
		}
		entry.path = append(entry.path, argumentPart{index: int(index), array: true})
	}
	if f.mode != "" {
		if f.indexCount() == 0 && raw != "true" {
			return entry, fmt.Errorf("--%s only accepts true; omit it to preserve the field", f.name)
		}
		switch f.mode {
		case "unset":
			entry.value = nil
		case "clear":
			if schemaType(f.schema) == "array" {
				entry.value = []any{}
			} else {
				entry.value = map[string]any{}
			}
		}
		return entry, nil
	}
	if scalarArray(f.schema) {
		// Preserve the old full-array syntax. Ordinary values, including commas,
		// empty strings and "null", are single elements and are never CSV-split.
		if strings.HasPrefix(strings.TrimSpace(raw), "[") {
			if value, err := decodeValue([]byte(raw)); err == nil {
				if _, ok := value.([]any); ok {
					entry.value = value
					return entry, nil
				}
			}
		}
		value, err := parseValue(raw, f.schema.Items)
		if err != nil {
			return entry, err
		}
		entry.value, entry.append = value, true
		return entry, nil
	}
	value, err := parseValue(raw, f.schema)
	entry.value = value
	return entry, err
}

func (f *field) assign(node *argumentNode) error {
	for _, entry := range f.entries {
		if err := node.assign(entry.path, entry.value, entry.append); err != nil {
			return err
		}
	}
	return nil
}

func scalarArray(s *jsonschema.Schema) bool {
	if schemaType(s) != "array" || s.Items == nil {
		return false
	}
	switch schemaType(s.Items) {
	case "string", "integer", "number", "boolean":
		return true
	}
	return false
}

func nullable(s *jsonschema.Schema) bool {
	if s == nil {
		return false
	}
	if s.Type == "null" || slices.Contains(s.Types, "null") {
		return true
	}
	for _, variants := range [][]*jsonschema.Schema{s.AnyOf, s.OneOf} {
		for _, variant := range variants {
			if nullable(variant) {
				return true
			}
		}
	}
	return false
}

func addFields(cmd, parent *cobra.Command, properties map[string]*jsonschema.Schema, binding Binding) ([]*field, error) {
	fields := []*field{}
	register := func(f *field) error {
		if !validName(f.name) || ancestorFlag(parent, f.name) || slices.Contains([]string{"output", "input", "help"}, f.name) || cmd.Flags().Lookup(f.name) != nil {
			return fmt.Errorf("flag collision or invalid name %q for %s", f.name, f.path)
		}
		description := safe(f.schema.Description)
		if description == "" {
			description = f.path
		}
		switch f.mode {
		case "clear":
			description = "Set " + f.path + " to an empty " + schemaType(f.schema)
		case "unset":
			description = "Set " + f.path + " to null"
		default:
			if scalarArray(f.schema) {
				description += " (repeat for each value; no comma splitting)"
			} else if slices.Contains([]string{"json", "array", "object"}, schemaType(f.schema)) {
				description += " (optional JSON value; use child flags when available)"
			}
		}
		if f.indexCount() > 0 {
			if f.mode != "" {
				description += "; supply zero-based index, nested indices separated by dots"
			} else {
				description += "; INDEX=VALUE, nested indices separated by dots"
			}
		}
		cmd.Flags().Var(f, f.name, description)
		if f.indexCount() == 0 && (f.mode != "" || schemaType(f.schema) == "boolean") {
			cmd.Flags().Lookup(f.name).NoOptDefVal = "true"
		}
		choices := []string{}
		choiceSchema := f.schema
		if scalarArray(choiceSchema) {
			choiceSchema = choiceSchema.Items
		}
		if f.mode == "" {
			for _, value := range choiceSchema.Enum {
				if text, ok := value.(string); ok {
					choices = append(choices, text)
				}
			}
		}
		if err := cmd.RegisterFlagCompletionFunc(f.name, func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if f.indexCount() == 0 {
				return choices, cobra.ShellCompDirectiveNoFileComp
			}
			prefix, _, found := strings.Cut(toComplete, "=")
			if !found {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			indexed := make([]string, len(choices))
			for i, choice := range choices {
				indexed[i] = prefix + "=" + choice
			}
			return indexed, cobra.ShellCompDirectiveNoFileComp
		}); err != nil {
			return err
		}
		fields = append(fields, f)
		return nil
	}
	var walk func(*jsonschema.Schema, string, string, int) error
	walk = func(property *jsonschema.Schema, path, name string, depth int) error {
		if alias, ok := binding.Flags[path]; ok {
			name = alias
		}
		if err := register(&field{path: path, name: name, schema: property}); err != nil {
			return err
		}
		if schemaType(property) == "array" || schemaType(property) == "object" {
			if err := register(&field{path: path, name: "clear-" + name, schema: property, mode: "clear"}); err != nil {
				return err
			}
		}
		if nullable(property) {
			if err := register(&field{path: path, name: "unset-" + name, schema: property, mode: "unset"}); err != nil {
				return err
			}
		}
		if depth >= 8 {
			return nil
		}
		child := property
		childPath, childName := path, strings.ReplaceAll(path, ".[]", "")
		if schemaType(property) == "array" && !scalarArray(property) && property.Items != nil {
			child = property.Items
			childPath += ".[]"
			if schemaType(child) == "array" {
				return walk(child, childPath, name+"-item", depth+1)
			}
		}
		keys := make([]string, 0, len(child.Properties))
		for key := range child.Properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := walk(child.Properties[key], childPath+"."+key, kebab(strings.ReplaceAll(childName+"."+key, ".", "-")), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := walk(properties[key], key, kebab(key), 0); err != nil {
			return nil, err
		}
	}
	return fields, nil
}

// Build arrays sparsely until all flags are collected, then require contiguous
// indices. An untrusted large index must not allocate a correspondingly large slice.
type argumentNode struct {
	value    any
	set      bool
	appended bool
	objects  map[string]*argumentNode
	arrays   map[int]*argumentNode
}

func (n *argumentNode) assign(path []argumentPart, value any, appendValue bool) error {
	if len(path) == 0 {
		if n.objects != nil || n.arrays != nil || (n.set && (!appendValue || !n.appended)) {
			return errors.New("duplicate or conflicting argument values")
		}
		if appendValue {
			if !n.set {
				n.value = []any{}
			}
			n.value = append(n.value.([]any), value)
		} else {
			n.value = value
		}
		n.set, n.appended = true, appendValue
		return nil
	}
	if n.set {
		return errors.New("conflicting parent and child argument values")
	}
	part := path[0]
	var child *argumentNode
	if part.array {
		if n.objects != nil {
			return errors.New("conflicting object and array arguments")
		}
		if n.arrays == nil {
			n.arrays = map[int]*argumentNode{}
		}
		child = n.arrays[part.index]
		if child == nil {
			child = &argumentNode{}
			n.arrays[part.index] = child
		}
	} else {
		if n.arrays != nil {
			return errors.New("conflicting array and object arguments")
		}
		if n.objects == nil {
			n.objects = map[string]*argumentNode{}
		}
		child = n.objects[part.key]
		if child == nil {
			child = &argumentNode{}
			n.objects[part.key] = child
		}
	}
	return child.assign(path[1:], value, appendValue)
}

func (n *argumentNode) materialize() (any, error) {
	if n.set {
		return n.value, nil
	}
	if n.arrays != nil {
		values := make([]any, len(n.arrays))
		for i := range values {
			child := n.arrays[i]
			if child == nil {
				return nil, fmt.Errorf("array indices must be contiguous from zero; missing index %d", i)
			}
			value, err := child.materialize()
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		return values, nil
	}
	if n.objects != nil {
		values := make(map[string]any, len(n.objects))
		for key, child := range n.objects {
			value, err := child.materialize()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			values[key] = value
		}
		return values, nil
	}
	return nil, nil
}
