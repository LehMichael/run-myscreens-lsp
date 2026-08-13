package syntax

import (
	"context"
	"testing"
)

func TestAnalyzeValidSource(t *testing.T) {
	analyzer := NewTreeSitterAnalyzer()
	diagnostics, err := analyzer.Analyze(context.Background(), []byte("//M(Test)\nDEF value=(I/0/1)\n//END\n"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestAnalyzeMismatchedEventEnd(t *testing.T) {
	analyzer := NewTreeSitterAnalyzer()
	diagnostics, err := analyzer.Analyze(context.Background(), []byte("//M(Test)\nLOAD\nEND_PRESS\n//END\n"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one", diagnostics)
	}
	if diagnostics[0].Code != "mismatched-event-end" {
		t.Fatalf("diagnostic code = %q, want mismatched-event-end", diagnostics[0].Code)
	}
}

func TestAnalyzeInvalidProse(t *testing.T) {
	analyzer := NewTreeSitterAnalyzer()
	diagnostics, err := analyzer.Analyze(context.Background(), []byte("WIP:ONE\n"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(diagnostics) == 0 || diagnostics[0].Code != "syntax-error" {
		t.Fatalf("diagnostics = %#v, want syntax-error", diagnostics)
	}
}
