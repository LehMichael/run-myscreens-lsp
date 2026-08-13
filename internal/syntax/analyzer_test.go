package syntax

import (
	"context"
	"testing"

	"example.com/run-myscreens-lsp/internal/model"
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
