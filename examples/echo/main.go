// Example: an offline schema supplies CLI help; Invoke supplies execution.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/OlegHQ/mcpcli"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	root, err := mcpcli.NewCommand(mcpcli.Options{
		Name: "echo-cli", Description: "Schema-derived CLI example",
		Tools:    []*mcp.Tool{{Name: "echo_message", Description: "Echo a message", InputSchema: json.RawMessage(`{"type":"object","required":["message"],"additionalProperties":false,"properties":{"message":{"type":"string","description":"Message to echo"}}}`)}},
		Bindings: map[string]mcpcli.Binding{"echo_message": {Path: []string{"echo"}, Positionals: []string{"message"}, Columns: []mcpcli.Column{{Header: "MESSAGE", Path: "message"}}}},
		Invoke: func(_ context.Context, _ string, args json.RawMessage) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{StructuredContent: args}, nil
		},
	})
	if err == nil {
		err = root.Execute()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
