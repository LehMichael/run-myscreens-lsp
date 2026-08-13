package syntax

import (
	"context"
	"fmt"
	"strings"

	"example.com/run-myscreens-lsp/internal/model"
	tree_sitter_run_myscreens "example.com/tree-sitter-run-myscreens/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

type Analyzer interface {
	Analyze(context.Context, []byte) (model.Analysis, error)
}

type TreeSitterAnalyzer struct {
	language *tree_sitter.Language
}

func NewTreeSitterAnalyzer() *TreeSitterAnalyzer {
	return &TreeSitterAnalyzer{
		language: tree_sitter.NewLanguage(tree_sitter_run_myscreens.Language()),
	}
}

func (a *TreeSitterAnalyzer) Analyze(ctx context.Context, source []byte) (model.Analysis, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(a.language); err != nil {
		return model.Analysis{}, fmt.Errorf("set Run MyScreens language: %w", err)
	}
	tree := parser.ParseCtx(ctx, source, nil)
	if tree == nil {
		if err := ctx.Err(); err != nil {
			return model.Analysis{}, err
		}
		return model.Analysis{}, fmt.Errorf("parse returned a nil tree")
	}
	defer tree.Close()

	root := tree.RootNode()
	analysis := model.Analysis{}
	collectDiagnostics(root, false, &analysis.Diagnostics)
	analysis.Symbols = collectSymbols(root, source)
	collectFolds(root, &analysis.Folds)
	return analysis, nil
}

func collectDiagnostics(node *tree_sitter.Node, insideError bool, diagnostics *[]model.Diagnostic) {
	isError := node.IsError()
	if isError && !insideError {
		*diagnostics = append(*diagnostics, model.Diagnostic{
			Range:   byteRange(node),
			Code:    "syntax-error",
			Message: "Syntax error",
		})
	}
	if node.IsMissing() {
		*diagnostics = append(*diagnostics, model.Diagnostic{
			Range:   byteRange(node),
			Code:    "missing-syntax",
			Message: fmt.Sprintf("Missing %s", node.Kind()),
		})
	}
	if node.Kind() == "mismatched_event_end" {
		*diagnostics = append(*diagnostics, model.Diagnostic{
			Range:   byteRange(node),
			Code:    "mismatched-event-end",
			Message: "Event block has a mismatched terminator",
		})
	}
	for index := uint(0); index < node.ChildCount(); index++ {
		child := node.Child(index)
		if child != nil {
			collectDiagnostics(child, insideError || isError, diagnostics)
		}
	}
}

func collectSymbols(node *tree_sitter.Node, source []byte) []model.Symbol {
	var symbols []model.Symbol
	for index := uint(0); index < node.NamedChildCount(); index++ {
		child := node.NamedChild(index)
		if child == nil {
			continue
		}
		if symbol, ok := lowerSymbol(child, source); ok {
			symbol.Children = collectSymbols(child, source)
			symbols = append(symbols, symbol)
			continue
		}
		symbols = append(symbols, collectSymbols(child, source)...)
	}
	return symbols
}

func lowerSymbol(node *tree_sitter.Node, source []byte) (model.Symbol, bool) {
	kind, detail, ok := symbolMetadata(node.Kind())
	if !ok {
		return model.Symbol{}, false
	}
	selection := symbolSelection(node, kind)
	name := nodeText(selection, source)
	if name == "" {
		name = detail
	}
	if node.Kind() == "declaration" {
		if version := node.ChildByFieldName("version"); version != nil {
			detail = strings.TrimSpace(detail + " " + nodeText(version, source))
		}
	}
	return model.Symbol{
		Name:           name,
		Detail:         detail,
		Kind:           kind,
		Range:          byteRange(node),
		SelectionRange: byteRange(selection),
	}, true
}

func symbolMetadata(nodeKind string) (model.SymbolKind, string, bool) {
	switch nodeKind {
	case "dialog_definition":
		return model.SymbolDialog, "dialog", true
	case "softkey_menu_definition":
		return model.SymbolSoftkeyMenu, "softkey menu", true
	case "array_definition":
		return model.SymbolArray, "array", true
	case "block_definition":
		return model.SymbolBlock, "reusable block", true
	case "grid_definition":
		return model.SymbolGrid, "grid", true
	case "declaration":
		return model.SymbolVariable, "variable", true
	case "subprogram":
		return model.SymbolSubprogram, "subprogram", true
	case "output_block":
		return model.SymbolOutput, "output", true
	case "load_event", "unload_event", "change_event", "press_event", "focus_event",
		"accesslevel_event", "channel_event", "control_event", "language_event",
		"resolution_event", "suspend_event", "resume_event", "start_softkey_press_event":
		return model.SymbolEvent, "event", true
	default:
		return model.SymbolUnknown, "", false
	}
}

func symbolSelection(node *tree_sitter.Node, kind model.SymbolKind) *tree_sitter.Node {
	if name := node.ChildByFieldName("name"); name != nil {
		return name
	}
	if header := node.ChildByFieldName("header"); header != nil {
		if kind == model.SymbolEvent {
			return header
		}
		if identifier := firstDescendant(header, "identifier", "start_softkey_identifier", "softkey_identifier"); identifier != nil {
			return identifier
		}
		return header
	}
	return node
}

func firstDescendant(node *tree_sitter.Node, kinds ...string) *tree_sitter.Node {
	for _, kind := range kinds {
		if node.Kind() == kind {
			return node
		}
	}
	for index := uint(0); index < node.NamedChildCount(); index++ {
		child := node.NamedChild(index)
		if child == nil {
			continue
		}
		if found := firstDescendant(child, kinds...); found != nil {
			return found
		}
	}
	return nil
}

func collectFolds(node *tree_sitter.Node, folds *[]model.Fold) {
	if isFoldable(node.Kind()) && node.StartPosition().Row < node.EndPosition().Row {
		*folds = append(*folds, model.Fold{Range: byteRange(node), Kind: model.FoldRegion})
	}
	for index := uint(0); index < node.NamedChildCount(); index++ {
		child := node.NamedChild(index)
		if child != nil {
			collectFolds(child, folds)
		}
	}
}

func isFoldable(kind string) bool {
	switch kind {
	case "dialog_definition", "softkey_menu_definition", "array_definition", "block_definition", "grid_definition",
		"load_event", "unload_event", "change_event", "press_event", "focus_event", "accesslevel_event",
		"channel_event", "control_event", "language_event", "resolution_event", "suspend_event", "resume_event",
		"start_softkey_press_event", "subprogram", "output_block", "if_statement", "switch_statement",
		"post_test_loop", "pre_test_loop":
		return true
	default:
		return false
	}
}

func byteRange(node *tree_sitter.Node) model.ByteRange {
	return model.ByteRange{Start: node.StartByte(), End: node.EndByte()}
}

func nodeText(node *tree_sitter.Node, source []byte) string {
	start, end := node.StartByte(), node.EndByte()
	if start > end || end > uint(len(source)) {
		return ""
	}
	return string(source[start:end])
}
