package mcpcli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const collectionSchema = `{
  "type":"object", "additionalProperties":false,
  "properties": {
    "labels":{"type":"array","items":{"type":"string"}},
    "counts":{"type":"array","items":{"type":"integer"}},
    "bits":{"type":"array","items":{"type":"boolean"}},
    "assignee":{"type":["string","null"]},
    "matrix":{"type":"array","items":{"type":"array","items":{"type":"integer"}}},
    "issues":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["title"],"properties":{
      "title":{"type":"string"},
      "enabled":{"type":"boolean"},
      "estimate":{"type":["integer","null"]},
      "assignee":{"type":["string","null"]},
      "labels":{"type":"array","items":{"type":"string"}},
      "filters":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["field"],"properties":{
        "field":{"type":"string","enum":["status","priority"]},
        "values":{"type":"array","items":{"type":"string"}}
      }}}
    }}}
  }
}`

func runCollection(t *testing.T, args ...string) (json.RawMessage, error) {
	t.Helper()
	var captured json.RawMessage
	root, err := NewCommand(Options{
		Name: "test", Tools: []*mcp.Tool{{Name: "apply", InputSchema: json.RawMessage(collectionSchema)}},
		Invoke: func(_ context.Context, _ string, input json.RawMessage) (*mcp.CallToolResult, error) {
			captured = input
			return &mcp.CallToolResult{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader(`{}`))
	root.SetArgs(append([]string{"apply"}, args...))
	err = root.Execute()
	return captured, err
}

func TestRepeatedScalarFlagsPreserveValues(t *testing.T) {
	got, err := runCollection(t, "--labels", "alpha,beta", "--labels", "null", "--labels", "", "--labels", "[literal]",
		"--counts", "9007199254740993", "--counts", "0", "--counts", "-7", "--bits", "true", "--bits", "false", "--assignee", "null")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"assignee":"null","bits":[true,false],"counts":[9007199254740993,0,-7],"labels":["alpha,beta","null","","[literal]"]}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestIndexedCollectionsAssembleIndependentOfFlagOrder(t *testing.T) {
	got, err := runCollection(t,
		"--issues-title", "1=Second=task", "--issues-title", "0=First",
		"--issues-labels", "0=bug", "--issues-labels", "0=urgent",
		"--issues-enabled", "0=false", "--issues-estimate", "0=9007199254740993",
		"--issues-filters-field", "0.1=priority", "--issues-filters-field", "0.0=status",
		"--issues-filters-values", "0.0=started", "--issues-filters-values", "0.0=unstarted",
		"--clear-issues-filters-values", "0.1", "--clear-issues-labels", "1",
		"--unset-issues-assignee", "1", "--unset-issues-estimate", "1")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"issues":[{"enabled":false,"estimate":9007199254740993,"filters":[{"field":"status","values":["started","unstarted"]},{"field":"priority","values":[]}],"labels":["bug","urgent"],"title":"First"},{"assignee":null,"estimate":null,"labels":[],"title":"Second=task"}]}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestClearUnsetAndOmission(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{nil, `{}`},
		{[]string{"--clear-labels", "--unset-assignee"}, `{"assignee":null,"labels":[]}`},
		{[]string{"--clear-issues"}, `{"issues":[]}`},
		{[]string{"--labels", `["old","JSON"]`}, `{"labels":["old","JSON"]}`},
		{[]string{"--matrix-item", "1=3", "--matrix-item", "0=1", "--matrix-item", "0=2"}, `{"matrix":[[1,2],[3]]}`},
		{[]string{"--clear-matrix-item", "0"}, `{"matrix":[[]]}`},
	} {
		got, err := runCollection(t, test.args...)
		if err != nil || string(got) != test.want {
			t.Fatalf("args %v: got %s, err %v, want %s", test.args, got, err, test.want)
		}
	}
}

func TestCollectionErrorsNeverInvoke(t *testing.T) {
	for _, args := range [][]string{
		{"--labels", "one", "--clear-labels"},
		{"--clear-labels", "--labels", "one"},
		{"--labels", `[]`, "--labels", "one"},
		{"--labels", "one", "--labels", `[]`},
		{"--assignee", "one", "--assignee", "two"},
		{"--assignee", "one", "--unset-assignee"},
		{"--clear-labels=false"},
		{"--unset-assignee=false"},
		{"--counts", "1.5"},
		{"--counts", "18446744073709551616"},
		{"--bits", "maybe"},
		{"--issues-title", "First"},
		{"--issues-title", "1=Gap"},
		{"--issues-title", "2147483647=Huge"},
		{"--issues-title", "00=Ambiguous"},
		{"--issues-title", "+0=Ambiguous"},
		{"--issues-title", "-1=Negative"},
		{"--issues-title", "0.1=Too deep"},
		{"--issues-title", "0=One", "--issues-title", "0=Two"},
		{"--issues-title", "0=One", "--issues-enabled", "0=wrong"},
		{"--issues-title", "0=One", "--issues-filters-field", "0=status"},
		{"--issues-title", "0=One", "--issues-filters-field", "0.0=unknown"},
		{"--issues-title", "0=One", "--issues-filters-field", "0.1=status"},
		{"--issues-title", "0=One", "--clear-issues"},
		{"--issues", `[{"title":"One"}]`, "--issues-title", "0=Two"},
		{"--issues-labels", "0=Missing required title"},
		{"--input", "-", "--clear-labels"},
		{"--input", "-", "--unset-assignee"},
		{"--input", "-", "--issues-title", "0=One"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got, err := runCollection(t, args...)
			if err == nil || got != nil {
				t.Fatalf("invalid input invoked tool: %s, err %v", got, err)
			}
		})
	}
}

func TestCollectionHelperCollisions(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","properties":{"labels":{"type":"array","items":{"type":"string"}},"clearLabels":{"type":"boolean"}}}`,
		`{"type":"object","properties":{"assignee":{"type":["string","null"]},"unsetAssignee":{"type":"boolean"}}}`,
		`{"type":"object","properties":{"issuesTitle":{"type":"string"},"issues":{"type":"array","items":{"type":"object","properties":{"title":{"type":"string"}}}}}}`,
	} {
		_, err := NewCommand(Options{Name: "test", Tools: []*mcp.Tool{{Name: "apply", InputSchema: json.RawMessage(schema)}},
			Invoke: func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) { return nil, nil }})
		if err == nil || !strings.Contains(err.Error(), "collision") {
			t.Fatalf("expected construction collision, got %v", err)
		}
	}
}

func TestAliasesAndObjectClearingPreservePatchIntent(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--labels", "bug", "--labels", "urgent", "--unset-assignee"}, `{"id":"i","patch":{"assignee":null,"labels":["bug","urgent"]}}`},
		{[]string{"--clear-patch"}, `{"id":"i","patch":{}}`},
	} {
		var got json.RawMessage
		opts := options(func(_ context.Context, _ string, input json.RawMessage) (*mcp.CallToolResult, error) {
			got = input
			return &mcp.CallToolResult{}, nil
		})
		opts.Bindings["update_issue"].Flags["patch.labels"] = "labels"
		opts.Bindings["update_issue"].Flags["patch.assignee"] = "assignee"
		root, err := NewCommand(opts)
		if err != nil {
			t.Fatal(err)
		}
		root.SetOut(&bytes.Buffer{})
		root.SetArgs(append([]string{"issue", "update", "i"}, test.args...))
		if err := root.Execute(); err != nil || string(got) != test.want {
			t.Fatalf("got %s, err %v, want %s", got, err, test.want)
		}
	}
}

func TestIndexedFlagCompletionAndHelpAreOffline(t *testing.T) {
	root, err := NewCommand(Options{Name: "test", Tools: []*mcp.Tool{{Name: "apply", InputSchema: json.RawMessage(collectionSchema)}},
		Invoke: func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			t.Fatal("help/completion invoked tool")
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	command, _, err := root.Find([]string{"apply"})
	if err != nil {
		t.Fatal(err)
	}
	complete, ok := command.GetFlagCompletionFunc("issues-filters-field")
	if !ok {
		t.Fatal("indexed leaf missing completion")
	}
	choices, _ := complete(command, nil, "0.1=")
	if strings.Join(choices, ",") != "0.1=status,0.1=priority" {
		t.Fatalf("bad indexed enum completion: %v", choices)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"apply", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--issues-title", "--clear-issues-labels", "--unset-issues-assignee", "INDEX=VALUE"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatalf("help missing %s", flag)
		}
	}
}

func TestClearStillValidatesSchemaAndAssembledInputLimit(t *testing.T) {
	for _, test := range []struct {
		schema string
		args   []string
		limit  int64
	}{
		{`{"type":"object","properties":{"labels":{"type":"array","minItems":1,"items":{"type":"string"}}}}`, []string{"--clear-labels"}, 1000},
		{collectionSchema, []string{"--labels", strings.Repeat("x", 100), "--labels", strings.Repeat("y", 100)}, 150},
	} {
		root, err := NewCommand(Options{Name: "test", MaxInputBytes: test.limit, Tools: []*mcp.Tool{{Name: "apply", InputSchema: json.RawMessage(test.schema)}},
			Invoke: func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
				t.Fatal("invalid collection invoked tool")
				return nil, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"apply"}, test.args...))
		if err := root.Execute(); err == nil {
			t.Fatal("accepted invalid collection")
		}
	}
}

func TestIndexedBindingAliasesAndLiteralText(t *testing.T) {
	var got json.RawMessage
	root, err := NewCommand(Options{Name: "test", Tools: []*mcp.Tool{{Name: "apply", InputSchema: json.RawMessage(collectionSchema)}},
		Bindings: map[string]Binding{"apply": {Flags: map[string]string{
			"issues.[].title":             "title",
			"issues.[].filters.[].values": "filter-values",
			"issues.[].filters.[].field":  "filter-field",
			"issues.[].assignee":          "owner",
		}}},
		Invoke: func(_ context.Context, _ string, input json.RawMessage) (*mcp.CallToolResult, error) {
			got = input
			return &mcp.CallToolResult{}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"apply", "--title", "0=First line\nSecond=a,b", "--filter-field", "0.0=status",
		"--filter-field", "0.1=priority", "--filter-values", "0.0=a,b=c\nd", "--clear-filter-values", "0.1", "--unset-owner", "0"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	want := `{"issues":[{"assignee":null,"filters":[{"field":"status","values":["a,b=c\nd"]},{"field":"priority","values":[]}],"title":"First line\nSecond=a,b"}]}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
