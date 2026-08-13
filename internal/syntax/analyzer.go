package syntax

import (
	"context"
	"fmt"
	"strings"

	"github.com/LehMichael/run-myscreens-lsp/internal/model"
	tree_sitter_run_myscreens "github.com/LehMichael/tree-sitter-run-myscreens/bindings/go"
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
	collectSemantics(root, source, nil, false, &analysis.Definitions, &analysis.References)
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

func collectSemantics(node *tree_sitter.Node, source []byte, scope []model.ByteRange, allowVariables bool, definitions *[]model.Definition, references *[]model.Reference) {
	if definition, ok := lowerDefinition(node, source, scope); ok {
		*definitions = append(*definitions, definition)
	}
	if reference, ok := lowerReference(node, source, scope, allowVariables); ok {
		*references = append(*references, reference)
	}

	childScope := scope
	if isSemanticScope(node.Kind()) {
		childScope = appendRange(scope, byteRange(node))
	}
	childrenAllowVariables := allowVariables || containsExecutableExpressions(node.Kind())
	for index := uint(0); index < node.NamedChildCount(); index++ {
		child := node.NamedChild(index)
		if child == nil {
			continue
		}
		childAllowsVariables := childrenAllowVariables && !suppressesVariableReferences(node, child)
		collectSemantics(child, source, childScope, childAllowsVariables, definitions, references)
	}
}

func lowerDefinition(node *tree_sitter.Node, source []byte, scope []model.ByteRange) (model.Definition, bool) {
	kind, ok := definitionKind(node.Kind())
	if !ok {
		return model.Definition{}, false
	}
	selection := definitionSelection(node)
	if selection == nil || selection.Kind() != "identifier" {
		return model.Definition{}, false
	}
	name := nodeText(selection, source)
	if name == "" {
		return model.Definition{}, false
	}
	return model.Definition{
		Name:           name,
		Kind:           kind,
		Range:          byteRange(node),
		SelectionRange: byteRange(selection),
		Scope:          appendRanges(nil, scope),
	}, true
}

func definitionKind(nodeKind string) (model.DefinitionKind, bool) {
	switch nodeKind {
	case "dialog_definition":
		return model.DefinitionDialog, true
	case "softkey_menu_definition":
		return model.DefinitionSoftkeyMenu, true
	case "array_definition":
		return model.DefinitionArray, true
	case "block_definition":
		return model.DefinitionBlock, true
	case "grid_definition":
		return model.DefinitionGrid, true
	case "declaration":
		return model.DefinitionVariable, true
	case "subprogram":
		return model.DefinitionSubprogram, true
	case "output_block":
		return model.DefinitionOutput, true
	default:
		return model.DefinitionUnknown, false
	}
}

func definitionSelection(node *tree_sitter.Node) *tree_sitter.Node {
	if name := node.ChildByFieldName("name"); name != nil {
		return name
	}
	header := node.ChildByFieldName("header")
	if header == nil {
		return nil
	}
	record := firstDescendant(header, "legacy_field_record", "property_record")
	if record == nil {
		return nil
	}
	for index := uint(0); index < record.ChildCount(); index++ {
		child := record.Child(index)
		if child == nil || !child.IsNamed() {
			continue
		}
		if child.Kind() == "identifier" {
			return child
		}
		if child.Kind() != "legacy_field" {
			return nil
		}
		identifier := singleNamedChild(child)
		if identifier != nil && identifier.Kind() == "identifier" {
			return identifier
		}
		return nil
	}
	return nil
}

func singleNamedChild(node *tree_sitter.Node) *tree_sitter.Node {
	if node.NamedChildCount() != 1 {
		return nil
	}
	return node.NamedChild(0)
}

func lowerReference(node *tree_sitter.Node, source []byte, scope []model.ByteRange, allowVariables bool) (model.Reference, bool) {
	if node.Kind() == "call_expression" {
		return lowerCallReference(node, source, scope)
	}
	if node.Kind() == "index_expression" {
		value := node.ChildByFieldName("value")
		if value == nil || value.Kind() != "identifier" {
			return model.Reference{}, false
		}
		return model.Reference{
			Name:  nodeText(value, source),
			Kind:  model.DefinitionArrayOrGrid,
			Range: byteRange(value),
			Scope: appendRanges(nil, scope),
		}, true
	}
	if node.Kind() == "identifier" && allowVariables {
		return model.Reference{
			Name:  nodeText(node, source),
			Kind:  model.DefinitionVariable,
			Range: byteRange(node),
			Scope: appendRanges(nil, scope),
		}, true
	}
	return model.Reference{}, false
}

func containsExecutableExpressions(nodeKind string) bool {
	switch nodeKind {
	case "assignment_statement", "expression_statement", "if_statement", "switch_statement", "case_clause",
		"post_test_loop", "pre_test_loop", "output_line", "load_event", "unload_event", "change_event",
		"press_event", "focus_event", "accesslevel_event", "channel_event", "control_event", "language_event",
		"resolution_event", "suspend_event", "resume_event", "start_softkey_press_event":
		return true
	default:
		return false
	}
}

func suppressesVariableReferences(parent, child *tree_sitter.Node) bool {
	switch parent.Kind() {
	case "dialog_definition", "softkey_menu_definition", "array_definition", "block_definition", "grid_definition":
		return sameNode(child, parent.ChildByFieldName("header"))
	case "declaration", "subprogram", "output_block":
		return sameNode(child, parent.ChildByFieldName("name")) || child.Kind() == "legacy_field_record" || child.Kind() == "property_record"
	case "call_expression":
		return sameNode(child, parent.ChildByFieldName("function"))
	case "member_expression":
		return sameNode(child, parent.ChildByFieldName("property"))
	case "index_expression":
		return sameNode(child, parent.ChildByFieldName("value"))
	case "property_assignment":
		return sameNode(child, parent.ChildByFieldName("name"))
	default:
		return false
	}
}

func sameNode(left, right *tree_sitter.Node) bool {
	return left != nil && right != nil && left.Kind() == right.Kind() && left.StartByte() == right.StartByte() && left.EndByte() == right.EndByte()
}

func lowerCallReference(node *tree_sitter.Node, source []byte, scope []model.ByteRange) (model.Reference, bool) {
	function := node.ChildByFieldName("function")
	arguments := node.ChildByFieldName("arguments")
	if function == nil || function.Kind() != "identifier" || arguments == nil {
		return model.Reference{}, false
	}

	var kind model.DefinitionKind
	fileSlot := -1
	switch strings.ToLower(nodeText(function, source)) {
	case "call":
		kind = model.DefinitionSubprogram
	case "gc":
		kind = model.DefinitionOutput
	case "lm":
		kind, fileSlot = model.DefinitionDialog, 1
	case "ls":
		kind, fileSlot = model.DefinitionSoftkeyMenu, 1
	case "lb":
		kind, fileSlot = model.DefinitionBlock, 1
	case "la":
		kind, fileSlot = model.DefinitionArray, 1
	case "lg":
		kind = model.DefinitionGrid
	default:
		return model.Reference{}, false
	}

	slots := argumentSlots(arguments)
	target := slots[0]
	if target == nil || target.Kind() != "string" {
		return model.Reference{}, false
	}
	name, ok := decodeString(target, source)
	if !ok || name == "" {
		return model.Reference{}, false
	}
	reference := model.Reference{
		Name:  name,
		Kind:  kind,
		Range: byteRange(target),
		Scope: appendRanges(nil, scope),
	}
	if fileSlot >= 0 {
		file := slots[fileSlot]
		if file != nil && file.Kind() == "string" {
			if targetFile, ok := decodeString(file, source); ok && targetFile != "" {
				reference.TargetFile = targetFile
				reference.FileRange = byteRange(file)
			}
		}
	}
	return reference, true
}

func argumentSlots(arguments *tree_sitter.Node) map[int]*tree_sitter.Node {
	slots := make(map[int]*tree_sitter.Node)
	slot := 0
	for index := uint(0); index < arguments.ChildCount(); index++ {
		child := arguments.Child(index)
		if child == nil {
			continue
		}
		if !child.IsNamed() && child.Kind() == "," {
			slot++
			continue
		}
		if child.IsNamed() && child.Kind() != "comment" {
			slots[slot] = child
		}
	}
	return slots
}

func decodeString(node *tree_sitter.Node, source []byte) (string, bool) {
	text := nodeText(node, source)
	if len(text) < 2 || text[0] != '"' || text[len(text)-1] != '"' {
		return "", false
	}
	return strings.ReplaceAll(text[1:len(text)-1], `""`, `"`), true
}

func isSemanticScope(nodeKind string) bool {
	switch nodeKind {
	case "dialog_definition", "softkey_menu_definition", "array_definition", "block_definition", "grid_definition", "subprogram", "output_block",
		"load_event", "unload_event", "change_event", "press_event", "focus_event", "accesslevel_event", "channel_event",
		"control_event", "language_event", "resolution_event", "suspend_event", "resume_event", "start_softkey_press_event":
		return true
	default:
		return false
	}
}

func appendRange(ranges []model.ByteRange, value model.ByteRange) []model.ByteRange {
	result := make([]model.ByteRange, len(ranges), len(ranges)+1)
	copy(result, ranges)
	return append(result, value)
}

func appendRanges(destination, source []model.ByteRange) []model.ByteRange {
	return append(destination, source...)
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
