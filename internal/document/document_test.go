package document

import (
	"testing"

	"github.com/LehMichael/run-myscreens-lsp/internal/model"
	"github.com/LehMichael/run-myscreens-lsp/internal/protocol"
)

func TestPositionAtUsesUTF16CodeUnits(t *testing.T) {
	document := New("file:///test.com", "run-myscreens", 1, "a😀ö\nβ")

	tests := []struct {
		byteOffset uint
		want       protocol.Position
	}{
		{0, protocol.Position{Line: 0, Character: 0}},
		{1, protocol.Position{Line: 0, Character: 1}},
		{5, protocol.Position{Line: 0, Character: 3}},
		{7, protocol.Position{Line: 0, Character: 4}},
		{8, protocol.Position{Line: 1, Character: 0}},
		{10, protocol.Position{Line: 1, Character: 1}},
	}
	for _, test := range tests {
		if got := document.PositionAt(test.byteOffset); got != test.want {
			t.Errorf("PositionAt(%d) = %#v, want %#v", test.byteOffset, got, test.want)
		}
	}
}

func TestByteOffsetAtUsesUTF16CodeUnits(t *testing.T) {
	document := New("file:///test.com", "run-myscreens", 1, "a😀ö\nβ")

	tests := []struct {
		position protocol.Position
		want     uint
		ok       bool
	}{
		{protocol.Position{Line: 0, Character: 0}, 0, true},
		{protocol.Position{Line: 0, Character: 1}, 1, true},
		{protocol.Position{Line: 0, Character: 3}, 5, true},
		{protocol.Position{Line: 0, Character: 4}, 7, true},
		{protocol.Position{Line: 1, Character: 1}, 10, true},
		{protocol.Position{Line: 0, Character: 2}, 0, false},
		{protocol.Position{Line: 2, Character: 0}, 0, false},
	}
	for _, test := range tests {
		got, ok := document.ByteOffsetAt(test.position)
		if got != test.want || ok != test.ok {
			t.Errorf("ByteOffsetAt(%#v) = %d, %v; want %d, %v", test.position, got, ok, test.want, test.ok)
		}
	}
}

func TestByteOffsetAtRejectsPositionInsideCRLF(t *testing.T) {
	document := New("file:///test.com", "run-myscreens", 1, "a\r\nb")
	if got, ok := document.ByteOffsetAt(protocol.Position{Line: 0, Character: 1}); !ok || got != 1 {
		t.Fatalf("line end = %d, %v; want 1, true", got, ok)
	}
	if _, ok := document.ByteOffsetAt(protocol.Position{Line: 0, Character: 2}); ok {
		t.Fatal("accepted a position inside CRLF")
	}
}

func TestReplaceReindexesLines(t *testing.T) {
	document := New("file:///test.com", "run-myscreens", 1, "one")
	document.Analysis = model.Analysis{Symbols: []model.Symbol{{Name: "old"}}}
	document.Replace(2, "one\ntwo")
	if got, want := document.PositionAt(7), (protocol.Position{Line: 1, Character: 3}); got != want {
		t.Fatalf("PositionAt after Replace = %#v, want %#v", got, want)
	}
	if document.Version != 2 {
		t.Fatalf("Version = %d, want 2", document.Version)
	}
	if len(document.Analysis.Symbols) != 0 {
		t.Fatalf("Replace retained stale analysis: %#v", document.Analysis)
	}
}

func TestStoreSetsAnalysisOnlyForCurrentVersion(t *testing.T) {
	store := NewStore()
	store.Open("file:///test.com", "run-myscreens", 1, "source")
	analysis := model.Analysis{Symbols: []model.Symbol{{Name: "Test"}}}
	if _, ok := store.SetAnalysis("file:///test.com", 2, analysis); ok {
		t.Fatal("SetAnalysis accepted stale version")
	}
	if _, ok := store.SetAnalysis("file:///test.com", 1, analysis); !ok {
		t.Fatal("SetAnalysis rejected current version")
	}
	document, ok := store.Get("file:///test.com")
	if !ok || len(document.Analysis.Symbols) != 1 || document.Analysis.Symbols[0].Name != "Test" {
		t.Fatalf("cached document = %#v, ok=%v", document, ok)
	}
}
