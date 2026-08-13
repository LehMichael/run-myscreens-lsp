package syntax

import (
	"context"
	"fmt"

	tree_sitter_run_myscreens "example.com/tree-sitter-run-myscreens/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

type Diagnostic struct {
	StartByte uint
	EndByte   uint
	Code      string
	Message   string
}

type Analyzer interface {
	Analyze(context.Context, []byte) ([]Diagnostic, error)
}

type TreeSitterAnalyzer struct {
	language *tree_sitter.Language
}

func NewTreeSitterAnalyzer() *TreeSitterAnalyzer {
	return &TreeSitterAnalyzer{
		language: tree_sitter.NewLanguage(tree_sitter_run_myscreens.Language()),
	}
}

func (a *TreeSitterAnalyzer) Analyze(ctx context.Context, source []byte) ([]Diagnostic, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(a.language); err != nil {
		return nil, fmt.Errorf("set Run MyScreens language: %w", err)
	}
	tree := parser.ParseCtx(ctx, source, nil)
	if tree == nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("parse returned a nil tree")
	}
	defer tree.Close()

	var diagnostics []Diagnostic
	collectDiagnostics(tree.RootNode(), false, &diagnostics)
	return diagnostics, nil
}

func collectDiagnostics(node *tree_sitter.Node, insideError bool, diagnostics *[]Diagnostic) {
	isError := node.IsError()
	if isError && !insideError {
		*diagnostics = append(*diagnostics, Diagnostic{
			StartByte: node.StartByte(),
			EndByte:   node.EndByte(),
			Code:      "syntax-error",
			Message:   "Syntax error",
		})
	}
	if node.IsMissing() {
		*diagnostics = append(*diagnostics, Diagnostic{
			StartByte: node.StartByte(),
			EndByte:   node.EndByte(),
			Code:      "missing-syntax",
			Message:   fmt.Sprintf("Missing %s", node.Kind()),
		})
	}
	if node.Kind() == "mismatched_event_end" {
		*diagnostics = append(*diagnostics, Diagnostic{
			StartByte: node.StartByte(),
			EndByte:   node.EndByte(),
			Code:      "mismatched-event-end",
			Message:   "Event block has a mismatched terminator",
		})
	}
	for index := uint(0); index < node.ChildCount(); index++ {
		child := node.Child(index)
		if child != nil {
			collectDiagnostics(child, insideError || isError, diagnostics)
		}
	}
}
