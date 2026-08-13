package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/LehMichael/run-myscreens-lsp/internal/model"
	"github.com/LehMichael/run-myscreens-lsp/internal/syntax"
)

func TestLoadAndResolveSameAndCrossFileDefinitions(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "main.com"), "//M(Main)\nLOAD\n  CALL(\"local\")\n  LM(\"TARGET\", \"shared.COM\")\nEND_LOAD\nSUB(Local)\nEND_SUB\n//END\n")
	sharedPath := filepath.Join(root, "Shared.com")
	writeTestFile(t, sharedPath, "//M(Target)\n//END\n")

	index := New()
	if err := index.Load(context.Background(), []string{FileURI(root)}, syntax.NewTreeSitterAnalyzer()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	mainURI := FileURI(filepath.Join(root, "main.com"))
	main, ok := index.Document(mainURI)
	if !ok {
		t.Fatal("main document was not indexed")
	}
	if len(main.Analysis.References) != 2 {
		t.Fatalf("references = %#v", main.Analysis.References)
	}

	local, ok := index.Resolve(mainURI, main.Analysis.References[0])
	if !ok || local.URI != mainURI || sourceText(local.Text, local.Range) != "Local" {
		t.Fatalf("local definition = %#v, ok=%v", local, ok)
	}
	crossFile, ok := index.Resolve(mainURI, main.Analysis.References[1])
	if !ok || crossFile.URI != FileURI(sharedPath) || sourceText(crossFile.Text, crossFile.Range) != "Target" {
		t.Fatalf("cross-file definition = %#v, ok=%v", crossFile, ok)
	}
}

func TestOverlayReplacesDiskAnalysisAndCloseRestoresIt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.com")
	writeTestFile(t, path, "//M(Disk)\n//END\n")
	index := New()
	analyzer := syntax.NewTreeSitterAnalyzer()
	if err := index.Load(context.Background(), []string{FileURI(root)}, analyzer); err != nil {
		t.Fatalf("Load: %v", err)
	}
	uri := FileURI(path)
	overlayText := "//M(Buffer)\n//END\n"
	overlayAnalysis, err := analyzer.Analyze(context.Background(), []byte(overlayText))
	if err != nil {
		t.Fatalf("Analyze overlay: %v", err)
	}
	index.Overlay(uri, overlayText, overlayAnalysis)

	document, ok := index.Document(uri)
	if !ok || document.Analysis.Definitions[0].Name != "Buffer" {
		t.Fatalf("overlay document = %#v, ok=%v", document, ok)
	}
	if err := index.RemoveOverlay(context.Background(), uri, analyzer); err != nil {
		t.Fatalf("RemoveOverlay: %v", err)
	}
	document, ok = index.Document(uri)
	if !ok || document.Analysis.Definitions[0].Name != "Disk" {
		t.Fatalf("restored document = %#v, ok=%v", document, ok)
	}
}

func TestRemoveOverlayRefreshesSavedDiskFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.com")
	writeTestFile(t, path, "//M(Old)\n//END\n")
	index := New()
	analyzer := syntax.NewTreeSitterAnalyzer()
	if err := index.Load(context.Background(), []string{FileURI(root)}, analyzer); err != nil {
		t.Fatalf("Load: %v", err)
	}
	uri := FileURI(path)
	text := "//M(New)\n//END\n"
	analysis, err := analyzer.Analyze(context.Background(), []byte(text))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	index.Overlay(uri, text, analysis)
	writeTestFile(t, path, text)
	if err := index.RemoveOverlay(context.Background(), uri, analyzer); err != nil {
		t.Fatalf("RemoveOverlay: %v", err)
	}
	document, ok := index.Document(uri)
	if !ok || document.Analysis.Definitions[0].Name != "New" {
		t.Fatalf("refreshed document = %#v, ok=%v", document, ok)
	}
}

func TestResolvePlainLocalVariable(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	scope := model.ByteRange{Start: 0, End: 100}
	index.Overlay(uri, "value", model.Analysis{
		Definitions: []model.Definition{{Name: "Value", Kind: model.DefinitionVariable, SelectionRange: model.ByteRange{Start: 0, End: 5}, Scope: []model.ByteRange{scope}}},
	})
	location, ok := index.Resolve(uri, model.Reference{Name: "value", Kind: model.DefinitionVariable, Scope: []model.ByteRange{scope}})
	if !ok || location.Range != (model.ByteRange{Start: 0, End: 5}) {
		t.Fatalf("Resolve = %#v, %v", location, ok)
	}
}

func TestResolveIndexedExpressionPrefersVisibleVariable(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	index.Overlay(uri, "ArrayName", model.Analysis{
		Definitions: []model.Definition{
			{Name: "ArrayName", Kind: model.DefinitionArray, SelectionRange: model.ByteRange{Start: 0, End: 5}},
			{Name: "ArrayName", Kind: model.DefinitionVariable, SelectionRange: model.ByteRange{Start: 5, End: 10}, Scope: []model.ByteRange{{Start: 0, End: 100}}},
		},
	})
	reference := model.Reference{Name: "arrayname", Kind: model.DefinitionArrayOrGrid, Scope: []model.ByteRange{{Start: 0, End: 100}}}
	location, ok := index.Resolve(uri, reference)
	if !ok || location.Range != (model.ByteRange{Start: 5, End: 10}) {
		t.Fatalf("Resolve = %#v, %v; want visible variable", location, ok)
	}
}

func TestResolveCallInExplicitlyLoadedBlockFile(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.com")
	writeTestFile(t, mainPath, "//M(Main)\nLOAD\n  LB(\"Helpers\", \"LIB/blocks.COM\")\n  CALL(\"work\")\nEND_LOAD\n//END\n")
	blockPath := filepath.Join(root, "Lib", "Blocks.com")
	if err := os.MkdirAll(filepath.Dir(blockPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, blockPath, "//B(Helpers)\nSUB(Work)\nEND_SUB\n//END\n")
	unrelatedPath := filepath.Join(root, "Other", "Blocks.com")
	if err := os.MkdirAll(filepath.Dir(unrelatedPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, unrelatedPath, "//B(Other)\nSUB(Wrong)\nEND_SUB\n//END\n")

	index := New()
	if err := index.Load(context.Background(), []string{FileURI(root)}, syntax.NewTreeSitterAnalyzer()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	mainURI := FileURI(mainPath)
	document, _ := index.Document(mainURI)
	callReference := document.Analysis.References[1]
	location, ok := index.Resolve(mainURI, callReference)
	if !ok || location.URI != FileURI(blockPath) || sourceText(location.Text, location.Range) != "Work" {
		t.Fatalf("loaded CALL definition = %#v, ok=%v", location, ok)
	}
}

func TestResolveDoesNotFallThroughAmbiguousLocalToLoadedDefinition(t *testing.T) {
	uri := "file:///workspace/main.com"
	loadedURI := "file:///workspace/blocks.com"
	index := New()
	index.Overlay(uri, "source", model.Analysis{
		Definitions: []model.Definition{
			{Name: "Duplicate", Kind: model.DefinitionSubprogram},
			{Name: "duplicate", Kind: model.DefinitionSubprogram},
		},
		References: []model.Reference{{Name: "Blocks", Kind: model.DefinitionBlock, TargetFile: "blocks.com"}},
	})
	index.Overlay(loadedURI, "loaded", model.Analysis{Definitions: []model.Definition{{Name: "Duplicate", Kind: model.DefinitionSubprogram}}})
	if _, ok := index.Resolve(uri, model.Reference{Name: "DUPLICATE", Kind: model.DefinitionSubprogram}); ok {
		t.Fatal("Resolve fell through ambiguous local definitions to a loaded file")
	}
}

func TestResolveDoesNotFallThroughAmbiguousVariablesToArray(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	scope := model.ByteRange{Start: 0, End: 100}
	index.Overlay(uri, "source", model.Analysis{Definitions: []model.Definition{
		{Name: "Value", Kind: model.DefinitionVariable, Scope: []model.ByteRange{scope}},
		{Name: "value", Kind: model.DefinitionVariable, Scope: []model.ByteRange{scope}},
		{Name: "Value", Kind: model.DefinitionArray},
	}})
	if _, ok := index.Resolve(uri, model.Reference{Name: "VALUE", Kind: model.DefinitionArrayOrGrid, Scope: []model.ByteRange{scope}}); ok {
		t.Fatal("Resolve fell through ambiguous variables to an array")
	}
}

func TestResolveReturnsNoResultForAmbiguousEntity(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	index.Overlay(uri, "source", model.Analysis{
		Definitions: []model.Definition{
			{Name: "Duplicate", Kind: model.DefinitionSubprogram},
			{Name: "duplicate", Kind: model.DefinitionSubprogram},
		},
	})
	if _, ok := index.Resolve(uri, model.Reference{Name: "DUPLICATE", Kind: model.DefinitionSubprogram}); ok {
		t.Fatal("Resolve returned an ambiguous definition")
	}
}

func TestPathFromFileURI(t *testing.T) {
	path, err := PathFromFileURI("file:///tmp/Run%20MyScreens")
	if err != nil {
		t.Fatalf("PathFromFileURI: %v", err)
	}
	if filepath.ToSlash(path) != "/tmp/Run MyScreens" {
		t.Fatalf("path = %q", path)
	}
	if _, err := PathFromFileURI("https://example.com/workspace"); err == nil {
		t.Fatal("PathFromFileURI accepted a non-file URI")
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func sourceText(text string, byteRange model.ByteRange) string {
	return text[byteRange.Start:byteRange.End]
}
