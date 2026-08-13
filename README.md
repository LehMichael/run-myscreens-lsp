# run-myscreens-lsp

Portable Go language server for Siemens SINUMERIK Run MyScreens source files.

## Architecture

Tree-sitter is isolated in `internal/syntax` and supplies concrete syntax trees only. The protocol, document model, semantic model, workspace index, diagnostics, completion, and navigation are native Go packages. This boundary allows the parser implementation to be replaced without rewriting the language server.

Current foundation:

- LSP/JSON-RPC over stdin/stdout
- `initialize`, `initialized`, `shutdown`, and `exit`
- Full-text `didOpen`, `didChange`, and `didClose`
- UTF-16-correct LSP positions
- Tree-sitter syntax, missing-node, and mismatched-event diagnostics
- Parser-independent semantic document model cached by document version
- Hierarchical document symbols for definitions, declarations, events, subprograms, and outputs
- Folding ranges for definitions, events, subprograms, conditionals, switches, loops, and outputs
- Diagnostic clearing when a document closes

The grammar dependency uses a local `replace` during development. Replace the temporary `example.com` module paths only after public repository ownership is decided.

## Development

```sh
go test ./...
go run ./cmd/run-myscreens-lsp
```
