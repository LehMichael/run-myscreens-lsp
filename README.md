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
- Recursive `.com` workspace indexing across initial workspace folders
- Open-document overlays with close-time disk refresh
- Case-insensitive, spelling-preserving definition lookup
- `textDocument/definition` for local variables, same-file `CALL`/`GC`, explicitly named `LM`/`LS`/`LB`/`LA` targets, and calls into explicitly loaded block files
- `textDocument/references` from declarations or references, with `includeDeclaration`, cross-file overlays, deterministic ordering, and the same conservative ambiguity rules
- Context-aware `textDocument/completion` for statement keywords/terminators, visible locals, static call/entity targets, and confirmed `.com` filename slots
- Safe completion text edits for incomplete and quoted source, including doubled-quote escaping and mid-token replacement
- `textDocument/hover` for declarations, references, explicit target files, and a conservative built-in catalog
- High-confidence semantic diagnostics for exact duplicate `DEF`s, missing explicit `.com` targets/entities, and provably out-of-scope local variables
- Diagnostic clearing and dependent open-document republishing when overlays change or close

Resolution and diagnostics are intentionally conservative: dynamic expressions, ambiguous targets, malformed analyses, arbitrary runtime symbols, and case-only declaration differences do not produce guessed results or hard errors. The grammar dependency is versioned from [`github.com/LehMichael/tree-sitter-run-myscreens`](https://github.com/LehMichael/tree-sitter-run-myscreens).

## Development

```sh
go test ./...
go run ./cmd/run-myscreens-lsp
```
