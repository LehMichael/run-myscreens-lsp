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

type DefinitionKind uint8

const (
	DefinitionUnknown DefinitionKind = iota
	DefinitionDialog
	DefinitionSoftkeyMenu
	DefinitionArray
	DefinitionBlock
	DefinitionGrid
	DefinitionVariable
	DefinitionSubprogram
	DefinitionOutput
	DefinitionArrayOrGrid
)

type Definition struct {
	Name           string
	Kind           DefinitionKind
	Range          ByteRange
	SelectionRange ByteRange
	Scope          []ByteRange
	Version        string
	Type           string
}

type Reference struct {
	Name       string
	Kind       DefinitionKind
	Range      ByteRange
	Scope      []ByteRange
	TargetFile string
	FileRange  ByteRange
}

type CompletionContextKind uint8

const (
	CompletionUnknown CompletionContextKind = iota
	CompletionStatement
	CompletionExpression
	CompletionTarget
	CompletionFilename
)

type CompletionContext struct {
	Kind                CompletionContextKind
	Prefix              string
	ReplaceRange        ByteRange
	Quoted              bool
	OwnerStart          uint
	HasOwner            bool
	Scope               []ByteRange
	TargetKind          DefinitionKind
	ExpectedTerminators []string
}

type CompletionItemKind uint8

const (
	CompletionItemUnknown CompletionItemKind = iota
	CompletionItemKeyword
	CompletionItemVariable
	CompletionItemFunction
	CompletionItemMethod
	CompletionItemModule
	CompletionItemFile
)

type CompletionItem struct {
	Label      string
	InsertText string
	Detail     string
	Kind       CompletionItemKind
}

type BuiltinKind uint8

const (
	BuiltinUnknown BuiltinKind = iota
	BuiltinKeyword
	BuiltinFunction
	BuiltinConstant
)

type BuiltinUse struct {
	Name  string
	Kind  BuiltinKind
	Range ByteRange
}

type Hover struct {
	Range    ByteRange
	Contents string
}

type Analysis struct {
	Diagnostics      []Diagnostic
	Symbols          []Symbol
	Folds            []Fold
	Definitions      []Definition
	References       []Reference
	Builtins         []BuiltinUse
	SemanticComplete bool
}
