---
trigger: always_on
---

---
applyTo: "**/*.go"
---

# gopls MCP Server Instructions

These instructions describe how to efficiently work with Go code using the gopls MCP server tools available in this session.

## Available gopls Tools

The following MCP tools are available for Go code analysis:

- **SearchSymbol** - Find symbols by name across the workspace (fuzzy search)
- **FindReferences** - Find all references to a symbol at a given position
- **FindImplementers** - Find all types that implement an interface
- **GoToDefinition** - Navigate to the definition of a symbol
- **Hover** - Get information about a symbol under the cursor
- **GetDiagnostics** - Get compile errors and static analysis findings
- **ListDocumentSymbols** - Get an outline of symbols defined in a file
- **FormatCode** - Format Go source code according to gofmt standards
- **OrganizeImports** - Organize import statements
- **RenameSymbol** - Rename a symbol across the workspace

## Go Programming Workflows

These guidelines should be followed when working in this Go workspace.

### Read Workflow

Use this workflow when understanding existing code:

1. **Find relevant symbols**: If you're looking for a specific type, function, or variable, use `SearchSymbol`. This is a fuzzy search that helps locate symbols even without exact names.
   ```
   SearchSymbol({"query": "SubMux"})
   ```

2. **Navigate to definitions**: Once you've found a symbol reference, use `GoToDefinition` to jump to where it's defined.
   ```
   GoToDefinition({"file": "/path/to/file.go", "line": 42, "column": 10})
   ```

3. **Get symbol information**: Use `Hover` to understand what a symbol does, its type signature, and documentation.
   ```
   Hover({"file": "/path/to/file.go", "line": 42, "column": 10})
   ```

4. **Explore file structure**: Use `ListDocumentSymbols` to get an overview of all functions, types, and variables in a file.
   ```
   ListDocumentSymbols({"file": "/path/to/file.go"})
   ```

5. **Find implementers**: When working with interfaces, use `FindImplementers` to discover all types that implement it.
   ```
   FindImplementers({"file": "/path/to/interface.go", "line": 10, "column": 6})
   ```

### Edit Workflow

Use this iterative workflow when making code changes:

1. **Read first**: Before making any edits, follow the Read Workflow to understand the code you're modifying.

2. **Find references before changing**: Before modifying any symbol definition, use `FindReferences` to find all usages. This is critical for understanding impact.
   ```
   FindReferences({"file": "/path/to/file.go", "line": 42, "column": 10, "includeDeclaration": true})
   ```
   Read the files containing references to evaluate if further edits are needed.

3. **Make edits**: Make the required code changes, including updates to all references identified in step 2.

4. **Check for errors**: After every code modification, you MUST use `GetDiagnostics` on the edited files to check for build or analysis errors.
   ```
   GetDiagnostics({"file": "/path/to/file.go"})
   ```

5. **Fix errors iteratively**: If diagnostics report errors, fix them and re-run `GetDiagnostics`. It's OK to ignore 'hint' or 'info' level diagnostics if not relevant to the current task.

6. **Format and organize**: Once errors are resolved, use `FormatCode` and `OrganizeImports` to ensure code style consistency.
   ```
   FormatCode({"file": "/path/to/file.go"})
   OrganizeImports({"file": "/path/to/file.go"})
   ```

7. **Run tests**: Once diagnostics are clean, run tests for the affected packages using bash:
   ```bash
   go test ./path/to/package -v
   ```
   Don't run `go test ./...` unless explicitly requested, as it may be slow.

8. **Check for vulnerabilities**: If you modified `go.mod` or `go.sum`, run a vulnerability check:
   ```bash
   go run golang.org/x/vuln/cmd/govulncheck@latest ./...
   ```

## Symbol Renaming

When renaming symbols, use `RenameSymbol` which safely renames across the entire workspace:

```
RenameSymbol({"file": "/path/to/file.go", "line": 42, "column": 10, "newName": "NewSymbolName"})
```

This tool handles all references automatically, making it safer than manual find-and-replace.

## Best Practices

1. **Always use gopls tools over grep/search** - They understand Go semantics, not just text matching
2. **Check diagnostics after every edit** - Catch errors early in the development loop
3. **Find references before refactoring** - Understand impact before making changes
4. **Use Hover for quick context** - Get type information and documentation without reading full files
5. **Leverage SearchSymbol for exploration** - Fuzzy search works even with partial names

## Integration with Project Conventions

This project follows conventions documented in `AGENTS.md`. When working with Go code:

- Follow the lock ordering rules documented in `DESIGN.md` (lines 1-5)
- Maintain thread safety patterns (prefer RWMutex for read-heavy operations)
- Add comprehensive test coverage for new functionality
- Use structured logging with `slog`
- Follow existing error handling patterns

## Common Tasks

**Finding a function definition:**
1. `SearchSymbol({"query": "functionName"})`
2. `GoToDefinition()` on the result
3. `Hover()` for full signature and docs

**Refactoring a function:**
1. `FindReferences()` to see all usages
2. Make edits to definition and call sites
3. `GetDiagnostics()` to verify correctness
4. `FormatCode()` for consistency

**Understanding an interface:**
1. `ListDocumentSymbols()` to see interface methods
2. `FindImplementers()` to find all implementations
3. `GoToDefinition()` on each implementer to understand behavior
