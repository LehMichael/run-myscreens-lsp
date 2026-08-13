package syntax

import (
	"context"
	"strings"
	"testing"

	"github.com/LehMichael/run-myscreens-lsp/internal/model"
)

func TestAnalyzeValidSource(t *testing.T) {
	analyzer := NewTreeSitterAnalyzer()
	analysis, err := analyzer.Analyze(context.Background(), []byte("//M(Test)\nDEF value=(I/0/1)\n//END\n"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", analysis.Diagnostics)
	}
}

func TestAnalyzeMismatchedEventEnd(t *testing.T) {
	analyzer := NewTreeSitterAnalyzer()
	analysis, err := analyzer.Analyze(context.Background(), []byte("//M(Test)\nLOAD\nEND_PRESS\n//END\n"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one", analysis.Diagnostics)
	}
	if analysis.Diagnostics[0].Code != "mismatched-event-end" {
		t.Fatalf("diagnostic code = %q, want mismatched-event-end", analysis.Diagnostics[0].Code)
	}
}

func TestAnalyzeInvalidProse(t *testing.T) {
	analyzer := NewTreeSitterAnalyzer()
	analysis, err := analyzer.Analyze(context.Background(), []byte("WIP:ONE\n"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.Diagnostics) == 0 || analysis.Diagnostics[0].Code != "syntax-error" {
		t.Fatalf("diagnostics = %#v, want syntax-error", analysis.Diagnostics)
	}
}

func TestAnalyzeLowersDefinitionsAndStaticReferences(t *testing.T) {
	source := []byte("//M(Main)\n" +
		"DEF Items=(I/0/1)\n" +
		"LOAD\n" +
		"  CALL(\"Update\")\n" +
		"  GC(\"A \"\"quoted\"\" output\")\n" +
		"  LM(\"Other\", \"Shared.COM\")\n" +
		"  LS(\"Menu\",,1)\n" +
		"  CALL(dynamicName)\n" +
		"  object.CALL(\"Ignored\")\n" +
		"  Items[0]=Items+1\n" +
		"END_LOAD\n" +
		"SUB(Update)\n" +
		"END_SUB\n" +
		"OUTPUT(A)\n" +
		"END_OUTPUT\n" +
		"//END\n")
	analysis, err := NewTreeSitterAnalyzer().Analyze(context.Background(), source)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", analysis.Diagnostics)
	}
	if len(analysis.Definitions) != 4 {
		t.Fatalf("definitions = %#v, want dialog, variable, subprogram, output", analysis.Definitions)
	}
	wantDefinitions := []struct {
		name  string
		kind  model.DefinitionKind
		scope int
	}{
		{"Main", model.DefinitionDialog, 0},
		{"Items", model.DefinitionVariable, 1},
		{"Update", model.DefinitionSubprogram, 1},
		{"A", model.DefinitionOutput, 1},
	}
	for index, want := range wantDefinitions {
		got := analysis.Definitions[index]
		if got.Name != want.name || got.Kind != want.kind || len(got.Scope) != want.scope {
			t.Errorf("definition %d = %#v, want name=%q kind=%d scope=%d", index, got, want.name, want.kind, want.scope)
		}
	}
	if len(analysis.References) != 7 {
		t.Fatalf("references = %#v, want four static calls plus dynamicName, indexed Items, and plain Items variables", analysis.References)
	}
	wantReferences := []struct {
		name       string
		kind       model.DefinitionKind
		targetFile string
	}{
		{"Update", model.DefinitionSubprogram, ""},
		{"A \"quoted\" output", model.DefinitionOutput, ""},
		{"Other", model.DefinitionDialog, "Shared.COM"},
		{"Menu", model.DefinitionSoftkeyMenu, ""},
		{"dynamicName", model.DefinitionVariable, ""},
		{"Items", model.DefinitionArrayOrGrid, ""},
		{"Items", model.DefinitionVariable, ""},
	}
	for index, want := range wantReferences {
		got := analysis.References[index]
		if got.Name != want.name || got.Kind != want.kind || got.TargetFile != want.targetFile || len(got.Scope) != 2 {
			t.Errorf("reference %d = %#v, want name=%q kind=%d target=%q scope=2", index, got, want.name, want.kind, want.targetFile)
		}
	}
}

func TestAnalyzeExtractsDeclarationMetadataAndBuiltins(t *testing.T) {
	source := []byte("//M(Main)\nDEF legacy(1)=(R1//1)\nDEF modern(2)={TYP=\"I\", VAL=2}\nLOAD\n  IF TRUE\n    CALL(\"Work\")\n  ENDIF\nEND_LOAD\nSUB(Work)\n  RETURN\nEND_SUB\n//END\n")
	analysis, err := NewTreeSitterAnalyzer().Analyze(context.Background(), source)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !analysis.SemanticComplete {
		t.Fatalf("analysis was unexpectedly incomplete: %#v", analysis.Diagnostics)
	}
	if len(analysis.Definitions) < 3 || analysis.Definitions[1].Version != "1" || analysis.Definitions[1].Type != "R1" || analysis.Definitions[2].Version != "2" || analysis.Definitions[2].Type != "I" {
		t.Fatalf("declaration metadata = %#v", analysis.Definitions)
	}
	want := map[string]bool{"DEF": true, "IF": true, "TRUE": true, "CALL": true, "SUB": true, "RETURN": true}
	for _, builtin := range analysis.Builtins {
		delete(want, builtin.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing builtins %v from %#v", want, analysis.Builtins)
	}
}

func TestAnalyzeMarksMalformedSourceSemanticallyIncomplete(t *testing.T) {
	analysis, err := NewTreeSitterAnalyzer().Analyze(context.Background(), []byte("//M(Main)\nLOAD\n  IF value\n"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if analysis.SemanticComplete {
		t.Fatal("malformed analysis was marked semantically complete")
	}
}

func TestAnalyzeScopesReferencesToTheirEvent(t *testing.T) {
	source := []byte("//M(Main)\n" +
		"LOAD\n" +
		"  LB(\"Helpers\", \"blocks.com\")\n" +
		"  CALL(\"Work\")\n" +
		"END_LOAD\n" +
		"PRESS(VS1)\n" +
		"  CALL(\"Work\")\n" +
		"END_PRESS\n" +
		"//END\n")
	analysis, err := NewTreeSitterAnalyzer().Analyze(context.Background(), source)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.References) != 3 {
		t.Fatalf("references = %#v", analysis.References)
	}
	load := analysis.References[0]
	loadCall := analysis.References[1]
	pressCall := analysis.References[2]
	if len(load.Scope) != 2 || len(loadCall.Scope) != 2 || load.Scope[1] != loadCall.Scope[1] {
		t.Fatalf("load scopes = %#v and %#v", load.Scope, loadCall.Scope)
	}
	if len(pressCall.Scope) != 2 || pressCall.Scope[1] == load.Scope[1] {
		t.Fatalf("press scope = %#v, load scope = %#v", pressCall.Scope, load.Scope)
	}
}

func TestAnalyzeLowersAllTopLevelDefinitionKinds(t *testing.T) {
	source := []byte("//M{Modern,HD=\"Mask\"}\n//END\n" +
		"//S(Menu)\n//END\n" +
		"//A(Values)\n//END\n" +
		"//B(Block)\n//END\n" +
		"//G(Grid)\n//END\n")
	analysis, err := NewTreeSitterAnalyzer().Analyze(context.Background(), source)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", analysis.Diagnostics)
	}
	want := []struct {
		name string
		kind model.DefinitionKind
	}{
		{"Modern", model.DefinitionDialog},
		{"Menu", model.DefinitionSoftkeyMenu},
		{"Values", model.DefinitionArray},
		{"Block", model.DefinitionBlock},
		{"Grid", model.DefinitionGrid},
	}
	if len(analysis.Definitions) != len(want) {
		t.Fatalf("definitions = %#v", analysis.Definitions)
	}
	for index, expected := range want {
		got := analysis.Definitions[index]
		if got.Name != expected.name || got.Kind != expected.kind {
			t.Errorf("definition %d = %#v, want name=%q kind=%d", index, got, expected.name, expected.kind)
		}
	}
}

func TestCompletionContextClassifiesTargetsFilesAndStatements(t *testing.T) {
	analyzer := NewTreeSitterAnalyzer()
	tests := []struct {
		name       string
		source     string
		kind       model.CompletionContextKind
		prefix     string
		targetKind model.DefinitionKind
	}{
		{"call target", "//M(Main)\nLOAD\n  CALL(\"Up", model.CompletionTarget, "Up", model.DefinitionSubprogram},
		{"output target", "//M(Main)\nLOAD\n  GC(\"Co", model.CompletionTarget, "Co", model.DefinitionOutput},
		{"entity target", "//M(Main)\nLOAD\n  LM(\"Ma", model.CompletionTarget, "Ma", model.DefinitionDialog},
		{"filename", "//M(Main)\nLOAD\n  LS(\"Menu\", \"sha", model.CompletionFilename, "sha", model.DefinitionUnknown},
		{"grid remains target", "//M(Main)\nLOAD\n  LG(\"Gr", model.CompletionTarget, "Gr", model.DefinitionGrid},
		{"statement", "//M(Main)\nLOAD\n  CA", model.CompletionStatement, "CA", model.DefinitionUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			completion, err := analyzer.CompletionContext(context.Background(), []byte(test.source), uint(len(test.source)))
			if err != nil {
				t.Fatalf("CompletionContext: %v", err)
			}
			if completion.Kind != test.kind || completion.Prefix != test.prefix || completion.TargetKind != test.targetKind {
				t.Fatalf("completion = %#v", completion)
			}
		})
	}
}

func TestCompletionContextRejectsCommentsStringsAndMemberCalls(t *testing.T) {
	analyzer := NewTreeSitterAnalyzer()
	for _, source := range []string{
		"//M(Main)\nLOAD\n  ; CALL(\"Wo",
		"//M(Main)\nLOAD\n  object.CALL(\"Wo",
		"//M(Main)\nLOAD\n  value=\"prefix CALL(\"Wo",
	} {
		completion, err := analyzer.CompletionContext(context.Background(), []byte(source), uint(len(source)))
		if err != nil {
			t.Fatalf("CompletionContext: %v", err)
		}
		if completion.Kind == model.CompletionTarget || completion.Kind == model.CompletionFilename {
			t.Errorf("source %q misclassified as %#v", source, completion)
		}
	}
}

func TestCompletionContextRecoversMalformedEOFStructure(t *testing.T) {
	source := "//M(Main)\nLOAD\n  CALL(\"Wo"
	completion, err := NewTreeSitterAnalyzer().CompletionContext(context.Background(), []byte(source), uint(len(source)))
	if err != nil {
		t.Fatalf("CompletionContext: %v", err)
	}
	if completion.Kind != model.CompletionTarget || len(completion.Scope) < 2 {
		t.Fatalf("completion = %#v", completion)
	}
	want := map[string]bool{"END_LOAD": true, "//END": true}
	for _, terminator := range completion.ExpectedTerminators {
		delete(want, terminator)
	}
	if len(want) != 0 {
		t.Fatalf("missing terminators %v from %#v", want, completion.ExpectedTerminators)
	}
}

func TestCompletionContextRecoversBOMAndNearestIncompleteOwner(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		ownerStart uint
	}{
		{"bom", "\uFEFF//M(Main)\nSUB(Work)\nEND_SUB\nLOAD\n  CALL(\"Wo", uint(len("\uFEFF"))},
		{"nearest", "//M(First)\nSUB(Wrong)\nEND_SUB\n//M(Second)\nSUB(Work)\nEND_SUB\nLOAD\n  CALL(\"Wo", uint(len("//M(First)\nSUB(Wrong)\nEND_SUB\n"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			completion, err := NewTreeSitterAnalyzer().CompletionContext(context.Background(), []byte(test.source), uint(len(test.source)))
			if err != nil {
				t.Fatalf("CompletionContext: %v", err)
			}
			if !completion.HasOwner || completion.OwnerStart != test.ownerStart {
				t.Fatalf("completion = %#v", completion)
			}
		})
	}
}

func TestCompletionContextReplacesQuotedPrefixAndDoubledQuote(t *testing.T) {
	source := []byte("//M(Main)\nLOAD\n  LM(\"A \"\"quoted\"\" na\")\nEND_LOAD\n//END\n")
	offset := uint(strings.Index(string(source), `na"`) + len("na"))
	completion, err := NewTreeSitterAnalyzer().CompletionContext(context.Background(), source, offset)
	if err != nil {
		t.Fatalf("CompletionContext: %v", err)
	}
	if !completion.Quoted || completion.Prefix != `A "quoted" na` || string(source[completion.ReplaceRange.Start:completion.ReplaceRange.End]) != `A ""quoted"" na` {
		t.Fatalf("completion = %#v, replacement = %q", completion, source[completion.ReplaceRange.Start:completion.ReplaceRange.End])
	}
	insideEscape := uint(strings.Index(string(source), `""quoted`) + 1)
	completion, err = NewTreeSitterAnalyzer().CompletionContext(context.Background(), source, insideEscape)
	if err != nil {
		t.Fatalf("CompletionContext inside escape: %v", err)
	}
	if completion.Kind != model.CompletionTarget || completion.ReplaceRange.Start >= insideEscape || completion.ReplaceRange.End <= insideEscape+1 {
		t.Fatalf("inside escape completion = %#v", completion)
	}
}

func TestCompletionContextIncludesScopeAndExpectedTerminators(t *testing.T) {
	source := "//M(Main)\nLOAD\n  IF value==1\n    \n  ENDIF\nEND_LOAD\n//END\n"
	offset := uint(len("//M(Main)\nLOAD\n  IF value==1\n    "))
	completion, err := NewTreeSitterAnalyzer().CompletionContext(context.Background(), []byte(source), offset)
	if err != nil {
		t.Fatalf("CompletionContext: %v", err)
	}
	if completion.Kind != model.CompletionStatement || len(completion.Scope) != 2 {
		t.Fatalf("completion = %#v", completion)
	}
	wantTerminators := map[string]bool{"ENDIF": true, "END_LOAD": true, "//END": true}
	for _, terminator := range completion.ExpectedTerminators {
		delete(wantTerminators, terminator)
	}
	if len(wantTerminators) != 0 {
		t.Fatalf("missing terminators %v from %#v", wantTerminators, completion.ExpectedTerminators)
	}
}

func TestLexicalRecoveryHandlesCommentedEndStartSoftkeyAndPostLoop(t *testing.T) {
	source := []byte("//S(Menu)\nPRESS(HS9)\n  \nEND_PRESS\n//END ; comment\n//M(Next)\nDO\nLOOP_UNTIL TRUE\n  ")
	completion, err := NewTreeSitterAnalyzer().CompletionContext(context.Background(), source, uint(len(source)))
	if err != nil {
		t.Fatalf("CompletionContext: %v", err)
	}
	if !completion.HasOwner || completion.OwnerStart != uint(strings.Index(string(source), "//M(Next)")) {
		t.Fatalf("owner = %#v", completion)
	}
	for _, terminator := range completion.ExpectedTerminators {
		if terminator == "END_PRESS" || terminator == "LOOP_WHILE" || terminator == "LOOP_UNTIL" {
			t.Fatalf("stale terminator %q in %#v", terminator, completion.ExpectedTerminators)
		}
	}
}

func TestExpectedTerminatorsCoverEventsAndLoopShapes(t *testing.T) {
	want := map[string]string{
		"unload_event": "END_UNLOAD", "change_event": "END_CHANGE", "focus_event": "END_FOCUS",
		"accesslevel_event": "END_ACCESSLEVEL", "channel_event": "END_CHANNEL", "control_event": "END_CONTROL",
		"language_event": "END_LANGUAGE", "resolution_event": "END_RESOLUTION", "suspend_event": "END_SUSPEND",
		"resume_event": "END_RESUME", "pre_test_loop": "LOOP",
	}
	for kind, terminator := range want {
		if got := expectedTerminator(kind); got != terminator {
			t.Errorf("expectedTerminator(%q) = %q, want %q", kind, got, terminator)
		}
	}
	post := expectedTerminators("post_test_loop")
	if len(post) != 2 || post[0] != "LOOP_WHILE" || post[1] != "LOOP_UNTIL" {
		t.Fatalf("post-test terminators = %#v", post)
	}
}

func TestAnalyzeLowersHierarchicalSymbolsAndFolds(t *testing.T) {
	source := []byte("//M(Main)\n" +
		"DEF value(1)=(I/0/1)\n" +
		"LOAD\n" +
		"  IF value==1\n" +
		"    CALL(\"Update\")\n" +
		"  ENDIF\n" +
		"END_LOAD\n" +
		"SUB(Update)\n" +
		"  RETURN\n" +
		"END_SUB\n" +
		"OUTPUT(Code,1)\n" +
		"  value\n" +
		"END_OUTPUT\n" +
		"//END\n")
	analysis, err := NewTreeSitterAnalyzer().Analyze(context.Background(), source)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", analysis.Diagnostics)
	}
	if len(analysis.Symbols) != 1 {
		t.Fatalf("top-level symbols = %#v, want one dialog", analysis.Symbols)
	}
	dialog := analysis.Symbols[0]
	if dialog.Name != "Main" || dialog.Kind != model.SymbolDialog {
		t.Fatalf("dialog = %#v", dialog)
	}
	if len(dialog.Children) != 4 {
		t.Fatalf("dialog children = %#v, want declaration, event, subprogram, output", dialog.Children)
	}
	want := []struct {
		name   string
		kind   model.SymbolKind
		detail string
	}{
		{"value", model.SymbolVariable, "variable (1)"},
		{"LOAD", model.SymbolEvent, "event"},
		{"Update", model.SymbolSubprogram, "subprogram"},
		{"Code", model.SymbolOutput, "output"},
	}
	for index, expected := range want {
		got := dialog.Children[index]
		if got.Name != expected.name || got.Kind != expected.kind || got.Detail != expected.detail {
			t.Errorf("child %d = %#v, want name=%q kind=%d detail=%q", index, got, expected.name, expected.kind, expected.detail)
		}
	}
	if len(analysis.Folds) != 5 {
		t.Fatalf("folds = %#v, want dialog, event, if, subprogram, output", analysis.Folds)
	}
}
