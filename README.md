# mcpcli

Turn supplied MCP tool schemas into usable Go command-line interfaces. Cobra owns commands, flags, help, and shell completion; the official MCP Go SDK defines tools/results; Google's JSON Schema library validates arguments. The application owns authentication, transports, discovery, and domain behavior.

```sh
go get github.com/OlegHQ/mcpcli
```

## Integrate

```go
root, err := mcpcli.NewCommand(mcpcli.Options{
    Name: "nudge",
    Description: "Work with Nudge",
    Tools: tools, // []*mcp.Tool, available offline
    Invoke: func(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
        return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
    },
    Bindings: map[string]mcpcli.Binding{
        "update_issue": {
            Path: []string{"issue", "update"},
            Positionals: []string{"id"},
            Flags: map[string]string{"patch.title": "title"},
            Columns: []mcpcli.Column{{Header: "ID", Path: "id"}},
        },
    },
})
```

The supplied schemas produce flag types, descriptions, string-enum completions, and validation. Explicit mappings make human command names intentional. Unmapped tools retain their tool name converted to kebab-case. `AddCommands(root, options)` adds generated commands to an existing Cobra tree and rejects command/flag collisions, including existing aliases and ancestor authentication/configuration flags. Treat a construction error as fatal and discard that command tree; construction may have already added preceding valid commands.

```sh
nudge issue update ISSUE-ID --title 'New title'
nudge issue update --input patch.json
cat patch.json | nudge issue update --input - --output json
nudge issue update --help
```

Help and completion do not call `Invoke`. Build a new command tree for each execution, as with ordinary mutable Cobra command objects. The executable controls exit codes: an execution error must produce a nonzero exit. `errors.Is(err, mcpcli.ErrTool)` distinguishes an MCP `isError` result.

Run the self-contained example:

```sh
go run ./examples/echo echo 'Hello'
go run ./examples/echo echo 'Hello' --output json
```

## Inputs

Only changed flags and supplied positionals become arguments; schema defaults are not eagerly copied. Omitted values, `false`, zero, empty strings, empty arrays and JSON `null` retain their meaning. Primitive fields accept typed flag values; arrays and objects accept JSON. Inline nested object properties additionally produce flags such as `--patch-title`; `Binding.Flags` can rename them.

`--input FILE` accepts one complete JSON object; `--input -` reads stdin. It cannot combine with argument flags or positionals. The default maximum input is 20,000,000 bytes, configurable through `Options.MaxInputBytes`. Input JSON numbers retain their original representation when passed to `Invoke`. Validation uses native 64-bit integers and floating-point numbers; larger integers/out-of-range numbers fail explicitly.

The complete schema is validated by `github.com/google/jsonschema-go`, including properties not expanded into flags. Nested properties reached only through `$ref` or unions do not become flattened child flags. `$ref`, unions, and more complex nested schemas can use the parent JSON flag or `--input`; this library does not invent a competing JSON Schema engine. Property names containing dots should use whole-object input, because binding/column paths use dot notation. Schema flags conflicting with reserved names (`input`, `output`, `help`) or inherited flags must be renamed through bindings. For example, alias a tool’s `url` property to `resource-url` when the application already uses `--url` for its server endpoint. Command and flag names must start with an ASCII letter or digit and contain only letters, digits, hyphens, and underscores.

## Outputs and errors

`--output table` renders human output with `text/tabwriter`. `Binding.Columns` selects stable useful fields; otherwise scalar keys are displayed. Common `data` and `items` envelopes are unwrapped for tables, with `nextCursor` displayed. Terminal control characters in human output are sanitized.

`--output json` emits the complete MCP `CallToolResult`, preserving both `structuredContent` and every content block. This is intentionally not a lossy text-only projection. Machine consumers can inspect `.structuredContent` directly.

Successful results go to Cobra's stdout writer. MCP tool errors go to its stderr writer and return `ErrTool`; transport errors propagate to the application's error boundary. No retry, prompt, credential logging, or automatic pagination occurs. Applications should sanitize private transport errors before exposing them. `Render(writer, result, format, columns)` is also available to existing commands.

The library installs `--output` only if it is not already inherited or present. `Options.OutputFormat` can supply an application resolver, for example to implement `auto` using its own terminal detection. It must return `table` or `json`.

## Design references

- [Cobra](https://github.com/spf13/cobra): command trees, help and shell completion.
- [Official MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk): protocol ownership and typed tool/result definitions.
- [CLI Guidelines](https://clig.dev/): stdout/stderr separation and stdin/file conventions.
- [GitHub CLI API command](https://cli.github.com/manual/gh_api): explicit JSON/file input and pagination controls.

## Development

```sh
go test ./...
go vet ./...
```

MIT licensed. This library does not embed server credentials, discover remote servers implicitly, execute shell strings, or require a daemon.
