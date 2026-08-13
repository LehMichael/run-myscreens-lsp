package model

type ByteRange struct {
	Start uint
	End   uint
}

type Diagnostic struct {
	Range   ByteRange
	Code    string
	Message string
}

type SymbolKind uint8

const (
	SymbolUnknown SymbolKind = iota
	SymbolDialog
	SymbolSoftkeyMenu
	SymbolArray
	SymbolBlock
	SymbolGrid
	SymbolVariable
	SymbolEvent
	SymbolSubprogram
	SymbolOutput
)

type Symbol struct {
	Name           string
	Detail         string
	Kind           SymbolKind
	Range          ByteRange
	SelectionRange ByteRange
	Children       []Symbol
}

type FoldKind string

const (
	FoldRegion  FoldKind = "region"
	FoldComment FoldKind = "comment"
)

type Fold struct {
	Range ByteRange
	Kind  FoldKind
}

type Analysis struct {
	Diagnostics []Diagnostic
	Symbols     []Symbol
	Folds       []Fold
}
