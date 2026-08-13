package syntax

import (
	"context"
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
