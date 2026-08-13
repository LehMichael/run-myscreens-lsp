module example.com/run-myscreens-lsp

go 1.22

require (
	example.com/tree-sitter-run-myscreens v0.0.0
	github.com/tree-sitter/go-tree-sitter v0.24.0
)

require github.com/mattn/go-pointer v0.0.1 // indirect

replace example.com/tree-sitter-run-myscreens => ../tree-sitter-run-myscreens
