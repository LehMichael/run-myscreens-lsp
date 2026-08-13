package document

import (
	"testing"

	"example.com/run-myscreens-lsp/internal/protocol"
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

func TestReplaceReindexesLines(t *testing.T) {
	document := New("file:///test.com", "run-myscreens", 1, "one")
	document.Replace(2, "one\ntwo")
	if got, want := document.PositionAt(7), (protocol.Position{Line: 1, Character: 3}); got != want {
		t.Fatalf("PositionAt after Replace = %#v, want %#v", got, want)
	}
	if document.Version != 2 {
		t.Fatalf("Version = %d, want 2", document.Version)
	}
}
