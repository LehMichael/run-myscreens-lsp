package syntax

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/LehMichael/run-myscreens-lsp/internal/model"
	tree_sitter_run_myscreens "github.com/LehMichael/tree-sitter-run-myscreens/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

type Analyzer interface {
	Analyze(context.Context, []byte) (model.Analysis, error)
	CompletionContext(context.Context, []byte, uint) (model.CompletionContext, error)
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

func (a *TreeSitterAnalyzer) CompletionContext(ctx context.Context, source []byte, offset uint) (model.CompletionContext, error) {
	if offset > uint(len(source)) {
		offset = uint(len(source))
	}
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(a.language); err != nil {
		return model.CompletionContext{}, fmt.Errorf("set Run MyScreens language: %w", err)
	}
	tree := parser.ParseCtx(ctx, source, nil)
	if tree == nil {
		if err := ctx.Err(); err != nil {
			return model.CompletionContext{}, err
		}
		return model.CompletionContext{}, fmt.Errorf("parse returned a nil tree")
	}
	defer tree.Close()

	completion := classifyCompletionLine(source, offset)
	nodeOffset := offset
	if nodeOffset == uint(len(source)) && nodeOffset > 0 {
		nodeOffset--
	}
	node := tree.RootNode().NamedDescendantForByteRange(nodeOffset, nodeOffset)
	for current := node; current != nil; current = current.Parent() {
		if isSemanticScope(current.Kind()) {
			completion.Scope = append(completion.Scope, byteRange(current))
		}
		for _, terminator := range expectedTerminators(current.Kind()) {
			completion.ExpectedTerminators = appendUnique(completion.ExpectedTerminators, terminator)
		}
	}
	reverseRanges(completion.Scope)
	completion = recoverCompletionStructure(tree.RootNode(), source, offset, completion)
	return completion, nil
}

func classifyCompletionLine(source []byte, offset uint) model.CompletionContext {
	lineStart := offset
	for lineStart > 0 && source[lineStart-1] != '\n' {
		lineStart--
	}
	line := source[lineStart:offset]
	prefix := identifierPrefix(line)
	trimmed := strings.TrimLeft(string(line), " \t\f")
	replaceEnd := offset
	for replaceEnd < uint(len(source)) && isIdentifierByte(source[replaceEnd]) {
		replaceEnd++
	}
	completion := model.CompletionContext{
		Kind:         model.CompletionExpression,
		Prefix:       prefix,
		ReplaceRange: model.ByteRange{Start: offset - uint(len(prefix)), End: replaceEnd},
	}
	if trimmed == prefix {
		completion.Kind = model.CompletionStatement
	}
	next := byte(0)
	if offset < uint(len(source)) {
		next = source[offset]
	}
	if function, slot, stringPrefix, stringStart, replaceNext, ok := activeCallArgument(line, next); ok {
		completion.Prefix = stringPrefix
		completion.Quoted = true
		completion.ReplaceRange = model.ByteRange{Start: lineStart + uint(stringStart), End: quotedReplacementEnd(source, offset, replaceNext)}
		switch strings.ToLower(function) {
		case "call":
			if slot == 0 {
				completion.Kind, completion.TargetKind = model.CompletionTarget, model.DefinitionSubprogram
			}
		case "gc":
			if slot == 0 {
				completion.Kind, completion.TargetKind = model.CompletionTarget, model.DefinitionOutput
			}
		case "lm":
			completion = classifyEntityArgument(completion, slot, model.DefinitionDialog)
		case "ls":
			completion = classifyEntityArgument(completion, slot, model.DefinitionSoftkeyMenu)
		case "lb":
			completion = classifyEntityArgument(completion, slot, model.DefinitionBlock)
		case "la":
			completion = classifyEntityArgument(completion, slot, model.DefinitionArray)
		case "lg":
			if slot == 0 {
				completion.Kind, completion.TargetKind = model.CompletionTarget, model.DefinitionGrid
			}
		}
	}
	return completion
}

func classifyEntityArgument(completion model.CompletionContext, slot int, kind model.DefinitionKind) model.CompletionContext {
	if slot == 0 {
		completion.Kind, completion.TargetKind = model.CompletionTarget, kind
	} else if slot == 1 {
		completion.Kind = model.CompletionFilename
	}
	return completion
}

func activeCallArgument(line []byte, next byte) (string, int, string, int, bool, bool) {
	type callState struct {
		name         string
		depth        int
		slot         int
		insideString bool
		stringStart  int
	}
	var stack []callState
	outsideString := false
	for index := 0; index < len(line); index++ {
		char := line[index]
		if len(stack) > 0 && stack[len(stack)-1].insideString {
			if char == '"' {
				if index+1 < len(line) && line[index+1] == '"' {
					index++
					continue
				}
				if index == len(line)-1 && next == '"' {
					continue
				}
				stack[len(stack)-1].insideString = false
			}
			continue
		}
		if outsideString {
			if char == '"' {
				if index+1 < len(line) && line[index+1] == '"' {
					index++
					continue
				}
				outsideString = false
			}
			continue
		}
		if char == ';' {
			break
		}
		switch char {
		case '"':
			if len(stack) > 0 {
				stack[len(stack)-1].insideString = true
				stack[len(stack)-1].stringStart = index + 1
			} else {
				outsideString = true
			}
		case '(':
			name, member := identifierBefore(line, index)
			if name != "" && !member {
				stack = append(stack, callState{name: name, depth: 1})
			} else if len(stack) > 0 {
				stack[len(stack)-1].depth++
			}
		case ')':
			if len(stack) > 0 {
				stack[len(stack)-1].depth--
				if stack[len(stack)-1].depth == 0 {
					stack = stack[:len(stack)-1]
				}
			}
		case ',':
			if len(stack) > 0 && stack[len(stack)-1].depth == 1 {
				stack[len(stack)-1].slot++
			}
		}
	}
	if len(stack) == 0 {
		return "", 0, "", 0, false, false
	}
	call := stack[len(stack)-1]
	if !call.insideString {
		return "", 0, "", 0, false, false
	}
	replaceNext := len(line) > call.stringStart && line[len(line)-1] == '"' && next == '"'
	return call.name, call.slot, strings.ReplaceAll(string(line[call.stringStart:]), `""`, `"`), call.stringStart, replaceNext, true
}

func quotedReplacementEnd(source []byte, offset uint, replaceNext bool) uint {
	end := offset
	if replaceNext {
		end++
	}
	for end < uint(len(source)) {
		if source[end] != '"' {
			if source[end] == '\n' || source[end] == '\r' {
				break
			}
			end++
			continue
		}
		if end+1 < uint(len(source)) && source[end+1] == '"' {
			end += 2
			continue
		}
		break
	}
	return end
}

func identifierPrefix(line []byte) string {
	start := len(line)
	for start > 0 && isIdentifierByte(line[start-1]) {
		start--
	}
	return string(line[start:])
}

func identifierBefore(line []byte, end int) (string, bool) {
	for end > 0 && (line[end-1] == ' ' || line[end-1] == '\t') {
		end--
	}
	start := end
	for start > 0 && isIdentifierByte(line[start-1]) {
		start--
	}
	memberIndex := start
	for memberIndex > 0 && (line[memberIndex-1] == ' ' || line[memberIndex-1] == '\t') {
		memberIndex--
	}
	member := memberIndex > 0 && line[memberIndex-1] == '.'
	return string(line[start:end]), member
}

func isIdentifierByte(char byte) bool {
	return char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
}

type lexicalBlock struct {
	kind  string
	start uint
}

func recoverCompletionStructure(root *tree_sitter.Node, source []byte, offset uint, completion model.CompletionContext) model.CompletionContext {
	blocks := lexicalBlocks(root, source[:offset])
	for _, block := range blocks {
		if isTopLevelDefinition(block.kind) {
			completion.OwnerStart, completion.HasOwner = block.start, true
		}
		for _, terminator := range expectedTerminators(block.kind) {
			completion.ExpectedTerminators = appendUnique(completion.ExpectedTerminators, terminator)
		}
		if !isSemanticScope(block.kind) {
			continue
		}
		rangeValue, ok := semanticRangeAtStart(root, block.kind, block.start)
		if !ok {
			rangeValue = model.ByteRange{Start: block.start, End: uint(len(source))}
		}
		if !containsRange(completion.Scope, rangeValue) {
			completion.Scope = append(completion.Scope, rangeValue)
		}
	}
	sort.Slice(completion.Scope, func(left, right int) bool {
		if completion.Scope[left].Start != completion.Scope[right].Start {
			return completion.Scope[left].Start < completion.Scope[right].Start
		}
		return completion.Scope[left].End > completion.Scope[right].End
	})
	return completion
}

func isTopLevelDefinition(kind string) bool {
	switch kind {
	case "dialog_definition", "softkey_menu_definition", "array_definition", "block_definition", "grid_definition":
		return true
	default:
		return false
	}
}

func lexicalBlocks(rootForRecovery *tree_sitter.Node, source []byte) []lexicalBlock {
	var blocks []lexicalBlock
	lineStart := 0
	for lineStart <= len(source) {
		lineEnd := lineStart
		for lineEnd < len(source) && source[lineEnd] != '\n' {
			lineEnd++
		}
		blockStart := lineStart
		rawLine := string(source[lineStart:lineEnd])
		if strings.HasPrefix(rawLine, "\uFEFF") {
			blockStart += len("\uFEFF")
		}
		line := strings.TrimSpace(stripLineComment(rawLine))
		line = strings.TrimPrefix(line, "\uFEFF")
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, ";") {
			// Comment-only line.
		} else if upper == "//END" {
			blocks = popLexical(blocks, "dialog_definition", "softkey_menu_definition", "array_definition", "block_definition", "grid_definition")
		} else if strings.HasPrefix(upper, "//M(") || strings.HasPrefix(upper, "//M{") {
			blocks = replaceTopLevel(blocks, lexicalBlock{kind: "dialog_definition", start: uint(blockStart)})
		} else if strings.HasPrefix(upper, "//S(") {
			blocks = replaceTopLevel(blocks, lexicalBlock{kind: "softkey_menu_definition", start: uint(blockStart)})
		} else if strings.HasPrefix(upper, "//A(") {
			blocks = replaceTopLevel(blocks, lexicalBlock{kind: "array_definition", start: uint(blockStart)})
		} else if strings.HasPrefix(upper, "//B(") {
			blocks = replaceTopLevel(blocks, lexicalBlock{kind: "block_definition", start: uint(blockStart)})
		} else if strings.HasPrefix(upper, "//G(") {
			blocks = replaceTopLevel(blocks, lexicalBlock{kind: "grid_definition", start: uint(blockStart)})
		} else if kind, ok := lexicalOpener(upper); ok {
			if kind == "press_event" {
				if _, found := semanticRangeAtStart(rootForRecovery, "start_softkey_press_event", uint(blockStart)); found {
					kind = "start_softkey_press_event"
				}
			}
			blocks = append(blocks, lexicalBlock{kind: kind, start: uint(blockStart)})
		} else if kinds, ok := lexicalClosers(upper); ok {
			blocks = popLexical(blocks, kinds...)
		}
		if lineEnd == len(source) {
			break
		}
		lineStart = lineEnd + 1
	}
	return blocks
}

func replaceTopLevel(blocks []lexicalBlock, block lexicalBlock) []lexicalBlock {
	for index := len(blocks) - 1; index >= 0; index-- {
		if isTopLevelDefinition(blocks[index].kind) {
			blocks = blocks[:index]
			break
		}
	}
	return append(blocks, block)
}

func lexicalOpener(line string) (string, bool) {
	word := firstWord(line)
	switch word {
	case "LOAD":
		return "load_event", true
	case "UNLOAD":
		return "unload_event", true
	case "CHANGE":
		return "change_event", true
	case "PRESS":
		return "press_event", true
	case "FOCUS":
		return "focus_event", true
	case "ACCESSLEVEL":
		return "accesslevel_event", true
	case "CHANNEL":
		return "channel_event", true
	case "CONTROL":
		return "control_event", true
	case "LANGUAGE":
		return "language_event", true
	case "RESOLUTION":
		return "resolution_event", true
	case "SUSPEND":
		return "suspend_event", true
	case "RESUME":
		return "resume_event", true
	case "SUB":
		return "subprogram", true
	case "OUTPUT":
		return "output_block", true
	case "IF":
		return "if_statement", true
	case "SWITCH":
		return "switch_statement", true
	case "DO":
		return "post_test_loop", true
	case "DO_WHILE", "DO_UNTIL":
		return "pre_test_loop", true
	default:
		return "", false
	}
}

func lexicalClosers(line string) ([]string, bool) {
	word := firstWord(line)
	switch word {
	case "END_LOAD":
		return []string{"load_event"}, true
	case "END_UNLOAD":
		return []string{"unload_event"}, true
	case "END_CHANGE":
		return []string{"change_event"}, true
	case "END_PRESS":
		return []string{"press_event", "start_softkey_press_event"}, true
	case "END_FOCUS":
		return []string{"focus_event"}, true
	case "END_ACCESSLEVEL":
		return []string{"accesslevel_event"}, true
	case "END_CHANNEL":
		return []string{"channel_event"}, true
	case "END_CONTROL":
		return []string{"control_event"}, true
	case "END_LANGUAGE":
		return []string{"language_event"}, true
	case "END_RESOLUTION":
		return []string{"resolution_event"}, true
	case "END_SUSPEND":
		return []string{"suspend_event"}, true
	case "END_RESUME":
		return []string{"resume_event"}, true
	case "END_SUB":
		return []string{"subprogram"}, true
	case "END_OUTPUT":
		return []string{"output_block"}, true
	case "ENDIF":
		return []string{"if_statement"}, true
	case "END_SWITCH":
		return []string{"switch_statement"}, true
	case "LOOP":
		return []string{"pre_test_loop"}, true
	case "LOOP_WHILE", "LOOP_UNTIL":
		return []string{"post_test_loop"}, true
	default:
		return nil, false
	}
}

func stripLineComment(line string) string {
	insideString := false
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case '"':
			if insideString && index+1 < len(line) && line[index+1] == '"' {
				index++
				continue
			}
			insideString = !insideString
		case ';':
			if !insideString {
				return line[:index]
			}
		}
	}
	return line
}

func firstWord(line string) string {
	if index := strings.IndexAny(line, " (\t"); index >= 0 {
		return line[:index]
	}
	return line
}

func popLexical(blocks []lexicalBlock, kinds ...string) []lexicalBlock {
	for index := len(blocks) - 1; index >= 0; index-- {
		for _, kind := range kinds {
			if blocks[index].kind == kind {
				return blocks[:index]
			}
		}
	}
	return blocks
}

func semanticRangeAtStart(node *tree_sitter.Node, kind string, start uint) (model.ByteRange, bool) {
	if node.Kind() == kind && node.StartByte() == start {
		return byteRange(node), true
	}
	for index := uint(0); index < node.NamedChildCount(); index++ {
		child := node.NamedChild(index)
		if child != nil {
			if result, ok := semanticRangeAtStart(child, kind, start); ok {
				return result, true
			}
		}
	}
	return model.ByteRange{}, false
}

func containsRange(ranges []model.ByteRange, value model.ByteRange) bool {
	for _, existing := range ranges {
		if existing == value {
			return true
		}
	}
	return false
}

func expectedTerminators(kind string) []string {
	if kind == "post_test_loop" {
		return []string{"LOOP_WHILE", "LOOP_UNTIL"}
	}
	if terminator := expectedTerminator(kind); terminator != "" {
		return []string{terminator}
	}
	return nil
}

func expectedTerminator(kind string) string {
	switch kind {
	case "dialog_definition", "softkey_menu_definition", "array_definition", "block_definition", "grid_definition":
		return "//END"
	case "load_event":
		return "END_LOAD"
	case "unload_event":
		return "END_UNLOAD"
	case "change_event":
		return "END_CHANGE"
	case "press_event", "start_softkey_press_event":
		return "END_PRESS"
	case "focus_event":
		return "END_FOCUS"
	case "accesslevel_event":
		return "END_ACCESSLEVEL"
	case "channel_event":
		return "END_CHANNEL"
	case "control_event":
		return "END_CONTROL"
	case "language_event":
		return "END_LANGUAGE"
	case "resolution_event":
		return "END_RESOLUTION"
	case "suspend_event":
		return "END_SUSPEND"
	case "resume_event":
		return "END_RESUME"
	case "subprogram":
		return "END_SUB"
	case "output_block":
		return "END_OUTPUT"
	case "if_statement":
		return "ENDIF"
	case "switch_statement":
		return "END_SWITCH"
	case "post_test_loop":
		return "LOOP_WHILE"
	case "pre_test_loop":
		return "LOOP"
	default:
		return ""
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func reverseRanges(ranges []model.ByteRange) {
	for left, right := 0, len(ranges)-1; left < right; left, right = left+1, right-1 {
		ranges[left], ranges[right] = ranges[right], ranges[left]
	}
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
