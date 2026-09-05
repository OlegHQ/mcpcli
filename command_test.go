package mcpcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

const testSchema = `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string"},"priority":{"type":"integer"},"enabled":{"type":"boolean"},"patch":{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"},"assignee":{"type":["string","null"]},"labels":{"type":"array","items":{"type":"string"}}}}}}`

func options(invoke InvokeFunc) Options {
	return Options{Name: "test", Tools: []*mcp.Tool{{Name: "update_issue", InputSchema: json.RawMessage(testSchema)}}, Invoke: invoke, Bindings: map[string]Binding{"update_issue": {Path: []string{"issue", "update"}, Positionals: []string{"id"}, Flags: map[string]string{"patch.title": "title"}}}}
}
func TestFlagsPreserveExplicitValuesAndOmission(t *testing.T) {
	var captured json.RawMessage
	opts := options(func(_ context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
		if name != "update_issue" {
			t.Fatal(name)
		}
		captured = args
		return &mcp.CallToolResult{StructuredContent: map[string]any{"id": "i"}}, nil
	})
	root, err := NewCommand(opts)
	if err != nil {
		t.Fatal(err)
	}
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"issue", "update", "i", "--priority", "0", "--enabled=false", "--title", "", "--patch-labels", "[]"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if string(captured) != `{"enabled":false,"id":"i","patch":{"labels":[],"title":""},"priority":0}` {
		t.Fatalf("patch intent changed: %s", captured)
	}
}
func TestInputPreservesNullAndLargeInteger(t *testing.T) {
	var captured json.RawMessage
	root, err := NewCommand(options(func(_ context.Context, _ string, args json.RawMessage) (*mcp.CallToolResult, error) {
		captured = args
		return &mcp.CallToolResult{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	root.SetIn(strings.NewReader(`{"id":"i","priority":9007199254740993,"patch":{"assignee":null,"labels":[]}}`))
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"issue", "update", "--input", "-"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(captured, []byte(`9007199254740993`)) || !bytes.Contains(captured, []byte(`"assignee":null`)) {
		t.Fatalf("input loses precision/null: %s", captured)
	}
}
func TestInvalidArgumentsNeverInvoke(t *testing.T) {
	cases := [][]string{{"issue", "update"}, {"issue", "update", "i", "--priority", "1.5"}, {"issue", "update", "i", "--unknown", "x"}, {"issue", "update", "i", "--id", "duplicate"}, {"issue", "update", "i", "--output", "invalid"}, {"issue", "update", "i", "--input", "-"}}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			calls := 0
			root, err := NewCommand(options(func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
				calls++
				return &mcp.CallToolResult{}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			root.SetErr(&bytes.Buffer{})
			root.SetIn(strings.NewReader(`{}`))
			root.SetArgs(args)
			if err := root.Execute(); err == nil || calls != 0 {
				t.Fatalf("invalid arguments invoked tool: calls=%d err=%v", calls, err)
			}
		})
	}
}
func TestInputBoundAndTrailingJSON(t *testing.T) {
	for _, input := range []string{`{"id":"i"} {}`, `null`, strings.Repeat(" ", 65)} {
		opts := options(func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			t.Fatal("invalid input invoked tool")
			return nil, nil
		})
		opts.MaxInputBytes = 64
		root, err := NewCommand(opts)
		if err != nil {
			t.Fatal(err)
		}
		root.SetIn(strings.NewReader(input))
		root.SetArgs([]string{"issue", "update", "--input", "-"})
		if err := root.Execute(); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
func TestToolErrorUsesStderrAndNonzeroError(t *testing.T) {
	root, err := NewCommand(options(func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "conflict"}}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&diagnostic)
	root.SetArgs([]string{"issue", "update", "i", "--output", "json"})
	if err := root.Execute(); !errors.Is(err, ErrTool) {
		t.Fatalf("tool failure treated as success: %v", err)
	}
	if out.Len() != 0 || !strings.Contains(diagnostic.String(), `"isError":true`) {
		t.Fatalf("invalid error streams: stdout=%s stderr=%s", &out, &diagnostic)
	}
}
func TestConstructionRejectsRouteAndFlagCollisions(t *testing.T) {
	opts := options(func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) { return nil, nil })
	opts.Tools = append(opts.Tools, &mcp.Tool{Name: "other", InputSchema: json.RawMessage(testSchema)})
	opts.Bindings["other"] = opts.Bindings["update_issue"]
	if _, err := NewCommand(opts); err == nil {
		t.Fatal("duplicate command path accepted")
	}
	opts.Tools = opts.Tools[:1]
	binding := opts.Bindings["update_issue"]
	binding.Flags = map[string]string{"id": "input"}
	opts.Bindings["update_issue"] = binding
	if _, err := NewCommand(opts); err == nil {
		t.Fatal("reserved flag collision accepted")
	}
}
func TestHumanAndJSONRender(t *testing.T) {
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "additional"}}, StructuredContent: json.RawMessage(`{"data":{"items":[{"id":"i","title":"bad\u001b[31m\nrow","count":9007199254740993}],"nextCursor":"next"}}`)}
	var out bytes.Buffer
	if err := Render(&out, result, "json", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `9007199254740993`) || !strings.Contains(out.String(), `additional`) {
		t.Fatalf("JSON result lost content: %s", &out)
	}
	out.Reset()
	if err := Render(&out, result, "table", []Column{{"ID", "id"}, {"TITLE", "title"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "Next cursor: next") {
		t.Fatalf("unsafe/incomplete table: %s", &out)
	}
}

func TestAncestorFlagAndAliasCollisionsRejected(t *testing.T) {
	invoke := func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) { return nil, nil }
	for _, persistent := range []bool{false, true} {
		root := &cobra.Command{Use: "app"}
		if persistent {
			root.PersistentFlags().String("url", "", "Endpoint")
		} else {
			root.Flags().String("url", "", "Endpoint")
		}
		opts := Options{Tools: []*mcp.Tool{{Name: "fetch", InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`)}}, Invoke: invoke}
		if err := AddCommands(root, opts); err == nil {
			t.Fatal("authentication flag shadowed")
		}
		opts.Bindings = map[string]Binding{"fetch": {Flags: map[string]string{"url": "resource-url"}}}
		if err := AddCommands(root, opts); err != nil {
			t.Fatalf("explicit nonconflicting flag alias: %v", err)
		}
	}
	root := &cobra.Command{Use: "app"}
	root.AddCommand(&cobra.Command{Use: "existing", Aliases: []string{"fetch"}})
	if err := AddCommands(root, Options{Tools: []*mcp.Tool{{Name: "fetch", InputSchema: json.RawMessage(`{"type":"object"}`)}}, Invoke: invoke}); err == nil {
		t.Fatal("command alias collision accepted")
	}
}
func TestUnsafeNamesAndNilPropertyRejected(t *testing.T) {
	invoke := func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) { return nil, nil }
	for _, tool := range []*mcp.Tool{
		{Name: "-unsafe", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "fetch", InputSchema: json.RawMessage(`{"type":"object","properties":{"-unsafe":{"type":"string"}}}`)},
		{Name: "fetch", InputSchema: json.RawMessage(`{"type":"object","properties":{"field":null}}`)},
	} {
		if _, err := NewCommand(Options{Name: "test", Tools: []*mcp.Tool{tool}, Invoke: invoke}); err == nil {
			t.Fatalf("invalid name/schema accepted: %s", tool.Name)
		}
	}
}
func TestReferenceAndUnionInputUsesSchemaValidator(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["patch"],"$defs":{"patch":{"type":"object","required":["value"],"properties":{"value":{"anyOf":[{"type":"integer"},{"type":"null"}]}}}},"properties":{"patch":{"$ref":"#/$defs/patch"}}}`)
	for _, input := range []string{`{"patch":{"value":null}}`, `{"patch":{"value":7}}`, `{"patch":{"value":"wrong"}}`} {
		calls := 0
		root, err := NewCommand(Options{Name: "test", Tools: []*mcp.Tool{{Name: "apply", InputSchema: schema}}, Invoke: func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			calls++
			return &mcp.CallToolResult{}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		root.SetIn(strings.NewReader(input))
		root.SetOut(&bytes.Buffer{})
		root.SetArgs([]string{"apply", "--input", "-"})
		err = root.Execute()
		valid := !strings.Contains(input, "wrong")
		if valid && (err != nil || calls != 1) {
			t.Fatalf("valid reference input rejected: %v", err)
		}
		if !valid && (err == nil || calls != 0) {
			t.Fatalf("union input not validated: %v", err)
		}
	}
}
func TestSharedOutputResolverAndUntruncatedDetails(t *testing.T) {
	root := &cobra.Command{Use: "app"}
	root.PersistentFlags().String("output", "auto", "")
	opts := options(func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: map[string]any{"detail": strings.Repeat("x", 10000)}}, nil
	})
	opts.OutputFormat = func(*cobra.Command) string { return "json" }
	if err := AddCommands(root, opts); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"issue", "update", "i"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), strings.Repeat("x", 10000)) {
		t.Fatal("structured details truncated")
	}
}
