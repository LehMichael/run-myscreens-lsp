package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestResolveLoadedCallUsesExactBlockAndOwningEntity(t *testing.T) {
	uri := "file:///workspace/main.com"
	blocksURI := "file:///workspace/blocks.com"
	dialogA := model.ByteRange{Start: 0, End: 100}
	dialogB := model.ByteRange{Start: 100, End: 200}
	loadEvent := model.ByteRange{Start: 10, End: 20}
	subprogram := model.ByteRange{Start: 20, End: 30}
	otherEvent := model.ByteRange{Start: 110, End: 120}
	loadedBlock := model.ByteRange{Start: 0, End: 50}
	unloadedBlock := model.ByteRange{Start: 50, End: 100}
	index := New()
	index.Overlay(uri, "source", model.Analysis{References: []model.Reference{
		{Name: "Loaded", Kind: model.DefinitionBlock, TargetFile: "blocks.com", Scope: []model.ByteRange{dialogA, loadEvent}},
	}})
	index.Overlay(blocksURI, "blocks", model.Analysis{Definitions: []model.Definition{
		{Name: "Loaded", Kind: model.DefinitionBlock, Range: loadedBlock, SelectionRange: model.ByteRange{Start: 1, End: 7}},
		{Name: "Work", Kind: model.DefinitionSubprogram, SelectionRange: model.ByteRange{Start: 10, End: 14}, Scope: []model.ByteRange{loadedBlock}},
		{Name: "Other", Kind: model.DefinitionBlock, Range: unloadedBlock, SelectionRange: model.ByteRange{Start: 51, End: 56}},
		{Name: "Work", Kind: model.DefinitionSubprogram, SelectionRange: model.ByteRange{Start: 60, End: 64}, Scope: []model.ByteRange{unloadedBlock}},
	}})
	location, ok := index.Resolve(uri, model.Reference{Name: "work", Kind: model.DefinitionSubprogram, Scope: []model.ByteRange{dialogA, subprogram}})
	if !ok || location.Range != (model.ByteRange{Start: 10, End: 14}) {
		t.Fatalf("loaded block call = %#v, %v", location, ok)
	}
	if _, ok := index.Resolve(uri, model.Reference{Name: "work", Kind: model.DefinitionSubprogram, Scope: []model.ByteRange{dialogB, otherEvent}}); ok {
		t.Fatal("call resolved through an LB from another scope")
	}
}

func TestResolveDeduplicatesRepeatedLoadsOfSameBlock(t *testing.T) {
	uri := "file:///workspace/main.com"
	blocksURI := "file:///workspace/blocks.com"
	dialog := model.ByteRange{Start: 0, End: 100}
	block := model.ByteRange{Start: 0, End: 50}
	load := model.Reference{Name: "Loaded", Kind: model.DefinitionBlock, TargetFile: "blocks.com", Scope: []model.ByteRange{dialog}}
	index := New()
	index.Overlay(uri, "source", model.Analysis{References: []model.Reference{load, load}})
	index.Overlay(blocksURI, "blocks", model.Analysis{Definitions: []model.Definition{
		{Name: "Loaded", Kind: model.DefinitionBlock, Range: block, SelectionRange: model.ByteRange{Start: 1, End: 7}},
		{Name: "Work", Kind: model.DefinitionSubprogram, SelectionRange: model.ByteRange{Start: 10, End: 14}, Scope: []model.ByteRange{block}},
	}})
	location, ok := index.Resolve(uri, model.Reference{Name: "work", Kind: model.DefinitionSubprogram, Scope: []model.ByteRange{dialog}})
	if !ok || location.Range != (model.ByteRange{Start: 10, End: 14}) {
		t.Fatalf("repeated load call = %#v, %v", location, ok)
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

func TestReferencesFromDefinitionAndReferencePositions(t *testing.T) {
	uri := "file:///workspace/main.com"
	text := "Value value VALUE"
	scope := model.ByteRange{Start: 0, End: 100}
	index := New()
	index.Overlay(uri, text, model.Analysis{
		Definitions: []model.Definition{{Name: "Value", Kind: model.DefinitionVariable, SelectionRange: model.ByteRange{Start: 0, End: 5}, Scope: []model.ByteRange{scope}}},
		References: []model.Reference{
			{Name: "value", Kind: model.DefinitionVariable, Range: model.ByteRange{Start: 6, End: 11}, Scope: []model.ByteRange{scope}},
			{Name: "VALUE", Kind: model.DefinitionVariable, Range: model.ByteRange{Start: 12, End: 17}, Scope: []model.ByteRange{scope}},
		},
	})

	locations, found, err := index.References(context.Background(), uri, 2, false)
	if err != nil || !found {
		t.Fatalf("References from definition = %#v, %v, %v", locations, found, err)
	}
	if len(locations) != 2 || locations[0].Range.Start != 6 || locations[1].Range.Start != 12 {
		t.Fatalf("references without declaration = %#v", locations)
	}
	locations, found, err = index.References(context.Background(), uri, 8, true)
	if err != nil || !found {
		t.Fatalf("References from reference = %#v, %v, %v", locations, found, err)
	}
	if len(locations) != 3 || locations[0].Range.Start != 0 || locations[1].Range.Start != 6 || locations[2].Range.Start != 12 {
		t.Fatalf("references with declaration = %#v", locations)
	}
}

func TestReferencesAcrossExplicitFileAndLoadedBlock(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.com")
	writeTestFile(t, mainPath, "//M(Main)\nLOAD\n  LM(\"target\", \"shared.COM\")\n  LB(\"helpers\", \"BLOCKS.com\")\n  CALL(\"work\")\nEND_LOAD\n//END\n")
	otherPath := filepath.Join(root, "other.com")
	writeTestFile(t, otherPath, "//M(Other)\nLOAD\n  LM(\"TARGET\", \"Shared.com\")\n  LB(\"Helpers\", \"blocks.COM\")\n  CALL(\"WORK\")\nEND_LOAD\n//END\n")
	sharedPath := filepath.Join(root, "Shared.com")
	writeTestFile(t, sharedPath, "//M(Target)\n//END\n")
	blocksPath := filepath.Join(root, "Blocks.com")
	writeTestFile(t, blocksPath, "//B(Helpers)\nSUB(Work)\nEND_SUB\n//END\n")

	index := New()
	if err := index.Load(context.Background(), []string{FileURI(root)}, syntax.NewTreeSitterAnalyzer()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	locations, found, err := index.References(context.Background(), FileURI(sharedPath), uint(len("//M(Ta")), true)
	if err != nil || !found || len(locations) != 3 {
		t.Fatalf("entity references = %#v, %v, %v", locations, found, err)
	}
	if locations[0].URI != FileURI(sharedPath) || locations[1].URI != FileURI(mainPath) || locations[2].URI != FileURI(otherPath) {
		t.Fatalf("entity reference order = %#v", locations)
	}
	locations, found, err = index.References(context.Background(), FileURI(blocksPath), uint(len("//B(Helpers)\nSUB(Wo")), false)
	if err != nil || !found || len(locations) != 2 {
		t.Fatalf("loaded CALL references = %#v, %v, %v", locations, found, err)
	}
	if locations[0].URI != FileURI(mainPath) || locations[1].URI != FileURI(otherPath) {
		t.Fatalf("CALL reference order = %#v", locations)
	}
}

func TestOverlayCollapsesCaseEquivalentDiskURI(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Shared.com")
	writeTestFile(t, path, "//M(Disk)\n//END\n")
	index := New()
	analyzer := syntax.NewTreeSitterAnalyzer()
	if err := index.Load(context.Background(), []string{FileURI(root)}, analyzer); err != nil {
		t.Fatalf("Load: %v", err)
	}
	text := "//M(Buffer)\n//END\n"
	analysis, err := analyzer.Analyze(context.Background(), []byte(text))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	aliasURI := FileURI(filepath.Join(root, "shared.com"))
	index.Overlay(aliasURI, text, analysis)
	document, ok := index.Document(FileURI(path))
	if !ok || document.URI != FileURI(path) || document.Analysis.Definitions[0].Name != "Buffer" {
		t.Fatalf("case-equivalent overlay = %#v, %v", document, ok)
	}
	locations, found, err := index.References(context.Background(), aliasURI, uint(len("//M(Bu")), true)
	if err != nil || !found || len(locations) != 1 || locations[0].URI != FileURI(path) {
		t.Fatalf("case-equivalent references = %#v, %v, %v", locations, found, err)
	}
}

func TestReferencesUseOverlaysAndRejectAmbiguousTargets(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	index.Overlay(uri, "Old old", model.Analysis{
		Definitions: []model.Definition{{Name: "Old", Kind: model.DefinitionSubprogram, SelectionRange: model.ByteRange{Start: 0, End: 3}}},
		References:  []model.Reference{{Name: "old", Kind: model.DefinitionSubprogram, Range: model.ByteRange{Start: 4, End: 7}}},
	})
	locations, found, err := index.References(context.Background(), uri, 1, false)
	if err != nil || !found || len(locations) != 1 || locations[0].Range.Start != 4 {
		t.Fatalf("overlay references = %#v, %v, %v", locations, found, err)
	}
	index.Overlay(uri, "Dup dup", model.Analysis{
		Definitions: []model.Definition{
			{Name: "Dup", Kind: model.DefinitionSubprogram, SelectionRange: model.ByteRange{Start: 0, End: 3}},
			{Name: "dup", Kind: model.DefinitionSubprogram, SelectionRange: model.ByteRange{Start: 0, End: 3}},
		},
		References: []model.Reference{{Name: "dup", Kind: model.DefinitionSubprogram, Range: model.ByteRange{Start: 4, End: 7}}},
	})
	locations, found, err = index.References(context.Background(), uri, 5, false)
	if err != nil || found || len(locations) != 0 {
		t.Fatalf("ambiguous references = %#v, %v, %v", locations, found, err)
	}
}

func TestReferencesRejectStaleRevision(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	index.Overlay(uri, "Old", model.Analysis{Definitions: []model.Definition{{Name: "Old", Kind: model.DefinitionDialog, SelectionRange: model.ByteRange{Start: 0, End: 3}}}})
	_, revision, ok := index.DocumentSnapshot(uri)
	if !ok {
		t.Fatal("DocumentSnapshot did not find overlay")
	}
	index.Overlay(uri, "New", model.Analysis{Definitions: []model.Definition{{Name: "New", Kind: model.DefinitionDialog, SelectionRange: model.ByteRange{Start: 0, End: 3}}}})
	locations, found, stale, err := index.ReferencesAtRevision(context.Background(), uri, 1, true, revision)
	if err != nil || !stale || found || locations != nil {
		t.Fatalf("stale references = %#v, %v, %v, %v", locations, found, stale, err)
	}
}

func TestReferencesHonorCancellation(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	index.Overlay(uri, "Target", model.Analysis{Definitions: []model.Definition{{Name: "Target", Kind: model.DefinitionDialog, SelectionRange: model.ByteRange{Start: 0, End: 6}}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	locations, found, err := index.References(ctx, uri, 1, false)
	if err != context.Canceled || found || locations != nil {
		t.Fatalf("canceled references = %#v, %v, %v", locations, found, err)
	}
}

func TestCompletionsIncludeVisibleLocalsAndLoadedTargets(t *testing.T) {
	uri := "file:///workspace/main.com"
	blocksURI := "file:///workspace/blocks.com"
	dialog := model.ByteRange{Start: 0, End: 100}
	event := model.ByteRange{Start: 10, End: 90}
	block := model.ByteRange{Start: 0, End: 50}
	index := New()
	index.Overlay(uri, "source", model.Analysis{
		Definitions: []model.Definition{
			{Name: "Value", Kind: model.DefinitionVariable, Scope: []model.ByteRange{dialog}},
			{Name: "value", Kind: model.DefinitionVariable, Scope: []model.ByteRange{dialog, event}},
			{Name: "EventOnly", Kind: model.DefinitionVariable, Scope: []model.ByteRange{dialog, event}},
			{Name: "Code", Kind: model.DefinitionOutput, Scope: []model.ByteRange{dialog}},
		},
		References: []model.Reference{{Name: "Helpers", Kind: model.DefinitionBlock, TargetFile: "blocks.com", Scope: []model.ByteRange{dialog, event}}},
	})
	index.Overlay(blocksURI, "blocks", model.Analysis{Definitions: []model.Definition{
		{Name: "Helpers", Kind: model.DefinitionBlock, Range: block, SelectionRange: model.ByteRange{Start: 1, End: 8}},
		{Name: "Work", Kind: model.DefinitionSubprogram, Scope: []model.ByteRange{block}},
	}})
	_, revision, _ := index.DocumentSnapshot(uri)
	items, stale, err := index.CompletionsAtRevision(context.Background(), uri, model.CompletionContext{Kind: model.CompletionExpression, Prefix: "v", Scope: []model.ByteRange{dialog, event}}, revision)
	if err != nil || stale || len(items) != 1 || items[0].Label != "value" {
		t.Fatalf("local completions = %#v, %v, %v", items, stale, err)
	}
	items, stale, err = index.CompletionsAtRevision(context.Background(), uri, model.CompletionContext{Kind: model.CompletionTarget, TargetKind: model.DefinitionSubprogram, Prefix: "wo", Scope: []model.ByteRange{dialog, event}}, revision)
	if err != nil || stale || len(items) != 1 || items[0].Label != "Work" || items[0].Kind != model.CompletionItemFunction {
		t.Fatalf("CALL completions = %#v, %v, %v", items, stale, err)
	}
	items, stale, err = index.CompletionsAtRevision(context.Background(), uri, model.CompletionContext{Kind: model.CompletionTarget, TargetKind: model.DefinitionOutput, Prefix: "co", Scope: []model.ByteRange{dialog, event}}, revision)
	if err != nil || stale || len(items) != 1 || items[0].Label != "Code" || items[0].Kind != model.CompletionItemMethod {
		t.Fatalf("GC completions = %#v, %v, %v", items, stale, err)
	}
}

func TestLocalCompletionDoesNotLeakAcrossSiblingEvents(t *testing.T) {
	uri := "file:///workspace/main.com"
	dialog := model.ByteRange{Start: 0, End: 100}
	press := model.ByteRange{Start: 10, End: 40}
	load := model.ByteRange{Start: 40, End: 90}
	index := New()
	index.Overlay(uri, "source", model.Analysis{Definitions: []model.Definition{
		{Name: "hidden", Kind: model.DefinitionVariable, Range: model.ByteRange{Start: 20, End: 26}, Scope: []model.ByteRange{dialog, press}},
	}})
	_, revision, _ := index.DocumentSnapshot(uri)
	completion := model.CompletionContext{Kind: model.CompletionExpression, Prefix: "hi", HasOwner: true, OwnerStart: 0, Scope: []model.ByteRange{dialog, load}}
	items, stale, err := index.CompletionsAtRevision(context.Background(), uri, completion, revision)
	if err != nil || stale || len(items) != 0 {
		t.Fatalf("sibling-event locals = %#v, %v, %v", items, stale, err)
	}
}

func TestCompletionsUseRecoveredOwnerForMalformedAnalysis(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	index.Overlay(uri, "source", model.Analysis{Definitions: []model.Definition{{Name: "Work", Kind: model.DefinitionSubprogram, Range: model.ByteRange{Start: 10, End: 20}}}})
	_, revision, _ := index.DocumentSnapshot(uri)
	completion := model.CompletionContext{
		Kind: model.CompletionTarget, TargetKind: model.DefinitionSubprogram, Prefix: "Wo",
		HasOwner: true, OwnerStart: 0, Scope: []model.ByteRange{{Start: 0, End: 100}},
	}
	items, stale, err := index.CompletionsAtRevision(context.Background(), uri, completion, revision)
	if err != nil || stale || len(items) != 1 || items[0].Label != "Work" {
		t.Fatalf("recovered owner completions = %#v, %v, %v", items, stale, err)
	}
}

func TestCompletionCandidatesInValidBOMSource(t *testing.T) {
	uri := "file:///workspace/main.com"
	text := "\uFEFF//M(Main)\nSUB(Work)\nEND_SUB\nLOAD\n  CALL(\"Wo\")\nEND_LOAD\n//END\n"
	analyzer := syntax.NewTreeSitterAnalyzer()
	analysis, err := analyzer.Analyze(context.Background(), []byte(text))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	offset := uint(strings.Index(text, `Wo"`) + len("Wo"))
	completion, err := analyzer.CompletionContext(context.Background(), []byte(text), offset)
	if err != nil {
		t.Fatalf("CompletionContext: %v", err)
	}
	index := New()
	index.Overlay(uri, text, analysis)
	_, revision, _ := index.DocumentSnapshot(uri)
	items, stale, err := index.CompletionsAtRevision(context.Background(), uri, completion, revision)
	if err != nil || stale || len(items) != 1 || items[0].Label != "Work" {
		t.Fatalf("BOM completions = %#v, %v, %v; context=%#v", items, stale, err, completion)
	}
}

func TestCompletionsIncludeEntitiesAndDeduplicatedFiles(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "Main.com")
	writeTestFile(t, mainPath, "//M(Main)\n//END\n")
	sharedPath := filepath.Join(root, "Shared.com")
	writeTestFile(t, sharedPath, "//M(SharedMask)\n//END\n")
	subdir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(subdir, "shared.com"), "//M(Other)\n//END\n")
	index := New()
	if err := index.Load(context.Background(), []string{FileURI(root)}, syntax.NewTreeSitterAnalyzer()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	uri := FileURI(mainPath)
	_, revision, _ := index.DocumentSnapshot(uri)
	items, stale, err := index.CompletionsAtRevision(context.Background(), uri, model.CompletionContext{Kind: model.CompletionTarget, TargetKind: model.DefinitionDialog, Prefix: "shared"}, revision)
	if err != nil || stale || len(items) != 1 || items[0].Label != "SharedMask" {
		t.Fatalf("entity completions = %#v, %v, %v", items, stale, err)
	}
	items, stale, err = index.CompletionsAtRevision(context.Background(), uri, model.CompletionContext{Kind: model.CompletionFilename, Prefix: ""}, revision)
	if err != nil || stale {
		t.Fatalf("filename completions = %#v, %v, %v", items, stale, err)
	}
	labels := make(map[string]bool)
	for _, item := range items {
		labels[item.Label] = true
	}
	if !labels["Shared.com"] || !labels["sub/shared.com"] {
		t.Fatalf("filename labels = %v", labels)
	}
	_ = sharedPath
}

func TestGridCompletionStaysInSameFile(t *testing.T) {
	uri := "file:///workspace/main.com"
	otherURI := "file:///workspace/other.com"
	index := New()
	index.Overlay(uri, "source", model.Analysis{Definitions: []model.Definition{{Name: "LocalGrid", Kind: model.DefinitionGrid}}})
	index.Overlay(otherURI, "other", model.Analysis{Definitions: []model.Definition{{Name: "RemoteGrid", Kind: model.DefinitionGrid}}})
	_, revision, _ := index.DocumentSnapshot(uri)
	items, stale, err := index.CompletionsAtRevision(context.Background(), uri, model.CompletionContext{Kind: model.CompletionTarget, TargetKind: model.DefinitionGrid}, revision)
	if err != nil || stale || len(items) != 1 || items[0].Label != "LocalGrid" {
		t.Fatalf("grid completions = %#v, %v, %v", items, stale, err)
	}
}

func TestCompletionsUseOverlayAndRejectStaleRevision(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	index.Overlay(uri, "old", model.Analysis{Definitions: []model.Definition{{Name: "Old", Kind: model.DefinitionDialog}}})
	_, revision, _ := index.DocumentSnapshot(uri)
	index.Overlay(uri, "new", model.Analysis{Definitions: []model.Definition{{Name: "New", Kind: model.DefinitionDialog}}})
	items, stale, err := index.CompletionsAtRevision(context.Background(), uri, model.CompletionContext{Kind: model.CompletionTarget, TargetKind: model.DefinitionDialog}, revision)
	if err != nil || !stale || items != nil {
		t.Fatalf("stale completions = %#v, %v, %v", items, stale, err)
	}
	_, revision, _ = index.DocumentSnapshot(uri)
	items, stale, err = index.CompletionsAtRevision(context.Background(), uri, model.CompletionContext{Kind: model.CompletionTarget, TargetKind: model.DefinitionDialog}, revision)
	if err != nil || stale || len(items) != 1 || items[0].Label != "New" {
		t.Fatalf("overlay completions = %#v, %v, %v", items, stale, err)
	}
}

func TestCallCompletionsHonorCancellation(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	index.Overlay(uri, "source", model.Analysis{})
	_, revision, _ := index.DocumentSnapshot(uri)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	items, stale, err := index.CompletionsAtRevision(ctx, uri, model.CompletionContext{Kind: model.CompletionTarget, TargetKind: model.DefinitionSubprogram}, revision)
	if err != context.Canceled || stale || items != nil {
		t.Fatalf("canceled CALL completions = %#v, %v, %v", items, stale, err)
	}
}

func TestTargetCompletionsHonorCancellation(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	index.Overlay(uri, "source", model.Analysis{})
	_, revision, _ := index.DocumentSnapshot(uri)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	items, stale, err := index.CompletionsAtRevision(ctx, uri, model.CompletionContext{Kind: model.CompletionTarget, TargetKind: model.DefinitionDialog}, revision)
	if err != context.Canceled || stale || items != nil {
		t.Fatalf("canceled target completions = %#v, %v, %v", items, stale, err)
	}
}

func TestCompletionsHonorCancellation(t *testing.T) {
	uri := "file:///workspace/main.com"
	index := New()
	index.Overlay(uri, "source", model.Analysis{})
	_, revision, _ := index.DocumentSnapshot(uri)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	items, stale, err := index.CompletionsAtRevision(ctx, uri, model.CompletionContext{Kind: model.CompletionFilename}, revision)
	if err != context.Canceled || stale || items != nil {
		t.Fatalf("canceled completions = %#v, %v, %v", items, stale, err)
	}
}

func TestSemanticDiagnosticsAreConservativeAndCaseSensitiveForDuplicates(t *testing.T) {
	uri := "file:///workspace/main.com"
	dialog := model.ByteRange{Start: 0, End: 200}
	firstEvent := model.ByteRange{Start: 10, End: 80}
	secondEvent := model.ByteRange{Start: 80, End: 160}
	index := New()
	index.Overlay(uri, "source", model.Analysis{SemanticComplete: true, Definitions: []model.Definition{
		{Name: "value", Kind: model.DefinitionVariable, SelectionRange: model.ByteRange{Start: 1, End: 6}, Scope: []model.ByteRange{dialog}, Version: "1"},
		{Name: "value", Kind: model.DefinitionVariable, SelectionRange: model.ByteRange{Start: 10, End: 15}, Scope: []model.ByteRange{dialog}, Version: "1"},
		{Name: "Value", Kind: model.DefinitionVariable, SelectionRange: model.ByteRange{Start: 20, End: 25}, Scope: []model.ByteRange{dialog}, Version: "1"},
		{Name: "temp", Kind: model.DefinitionVariable, SelectionRange: model.ByteRange{Start: 30, End: 34}, Scope: []model.ByteRange{dialog, firstEvent}},
	}, References: []model.Reference{
		{Name: "temp", Kind: model.DefinitionVariable, Range: model.ByteRange{Start: 90, End: 94}, Scope: []model.ByteRange{dialog, secondEvent}},
		{Name: "runtimeName", Kind: model.DefinitionVariable, Range: model.ByteRange{Start: 100, End: 111}, Scope: []model.ByteRange{dialog, secondEvent}},
	}})
	_, revision, _ := index.DocumentSnapshot(uri)
	diagnostics, stale, err := index.SemanticDiagnosticsAtRevision(context.Background(), uri, revision)
	if err != nil || stale || len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v, stale=%v, err=%v", diagnostics, stale, err)
	}
	if diagnostics[0].Code != "duplicate-def" || diagnostics[0].Range.Start != 10 || diagnostics[1].Code != "undefined-local-variable" || diagnostics[1].Range.Start != 90 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestSemanticDiagnosticsDistinguishMissingAmbiguousAndMalformedTargets(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "Main.com")
	sharedPath := filepath.Join(root, "Shared.com")
	writeTestFile(t, mainPath, "//M(Main)\nLOAD\n  LM(\"MissingEntity\", \"Shared.com\")\n  LS(\"Menu\", \"Absent.com\")\nEND_LOAD\n//END\n")
	writeTestFile(t, sharedPath, "//M(Other)\n//END\n")
	firstAmbiguousDir := filepath.Join(root, "first")
	secondAmbiguousDir := filepath.Join(root, "second")
	if err := os.MkdirAll(firstAmbiguousDir, 0o700); err != nil {
		t.Fatalf("mkdir first: %v", err)
	}
	if err := os.MkdirAll(secondAmbiguousDir, 0o700); err != nil {
		t.Fatalf("mkdir second: %v", err)
	}
	writeTestFile(t, filepath.Join(firstAmbiguousDir, "Duplicate.com"), "//M(One)\n//END\n")
	writeTestFile(t, filepath.Join(secondAmbiguousDir, "duplicate.COM"), "//M(Two)\n//END\n")
	index := New()
	analyzer := syntax.NewTreeSitterAnalyzer()
	if err := index.Load(context.Background(), []string{FileURI(root)}, analyzer); err != nil {
		t.Fatalf("Load: %v", err)
	}
	uri := FileURI(mainPath)
	_, revision, _ := index.DocumentSnapshot(uri)
	diagnostics, stale, err := index.SemanticDiagnosticsAtRevision(context.Background(), uri, revision)
	if err != nil || stale || len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v, stale=%v, err=%v", diagnostics, stale, err)
	}
	codes := map[string]bool{}
	for _, diagnostic := range diagnostics {
		codes[diagnostic.Code] = true
	}
	if !codes["missing-target-file"] || !codes["missing-target-entity"] {
		t.Fatalf("diagnostic codes = %v", codes)
	}

	ambiguousText := "//M(Main)\nLOAD\n  LM(\"Missing\", \"duplicate.com\")\nEND_LOAD\n//END\n"
	ambiguousAnalysis, err := analyzer.Analyze(context.Background(), []byte(ambiguousText))
	if err != nil {
		t.Fatalf("Analyze ambiguous: %v", err)
	}
	index.Overlay(uri, ambiguousText, ambiguousAnalysis)
	_, revision, _ = index.DocumentSnapshot(uri)
	diagnostics, stale, err = index.SemanticDiagnosticsAtRevision(context.Background(), uri, revision)
	if err != nil || stale || len(diagnostics) != 0 {
		t.Fatalf("ambiguous diagnostics = %#v, stale=%v, err=%v", diagnostics, stale, err)
	}

	malformedText := "//M(Main)\nLOAD\n  LM(\"Missing\", \"Absent.com\")\n"
	malformedAnalysis, err := analyzer.Analyze(context.Background(), []byte(malformedText))
	if err != nil {
		t.Fatalf("Analyze malformed: %v", err)
	}
	if malformedAnalysis.SemanticComplete {
		t.Fatal("malformed analysis marked complete")
	}
	index.Overlay(uri, malformedText, malformedAnalysis)
	_, revision, _ = index.DocumentSnapshot(uri)
	diagnostics, stale, err = index.SemanticDiagnosticsAtRevision(context.Background(), uri, revision)
	if err != nil || stale || len(diagnostics) != 0 {
		t.Fatalf("malformed diagnostics = %#v, stale=%v, err=%v", diagnostics, stale, err)
	}
}

func TestHoverShowsDefinitionReferenceFileAndBuiltin(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "Main.com")
	sharedPath := filepath.Join(root, "Shared.com")
	mainText := "//M(Main)\nDEF value(2)=(R1//1)\nLOAD\n  IF TRUE\n    value=1\n    LM(\"TARGET\", \"shared.COM\")\n  ENDIF\nEND_LOAD\n//END\n"
	writeTestFile(t, mainPath, mainText)
	writeTestFile(t, sharedPath, "//M(Target)\n//END\n")
	index := New()
	if err := index.Load(context.Background(), []string{FileURI(root)}, syntax.NewTreeSitterAnalyzer()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	uri := FileURI(mainPath)
	_, revision, _ := index.DocumentSnapshot(uri)
	tests := []struct {
		needle   string
		advance  int
		contains []string
	}{
		{"value(2)", 1, []string{"variable", "value", "Type: `R1`", "Version: `2`", "dialog `Main`"}},
		{"value=1", 1, []string{"variable reference", "Resolves to: variable `value`", "Type: `R1`"}},
		{"TARGET", 1, []string{"dialog reference", "Resolves to: dialog `Target`", "File: `Shared.com`"}},
		{"shared.COM", 1, []string{"Run MyScreens file", "Resolved file: `Shared.com`"}},
		{"IF TRUE", 1, []string{"keyword", "IF", "conditional block"}},
	}
	for _, test := range tests {
		offset := uint(strings.Index(mainText, test.needle) + test.advance)
		hover, found, stale, err := index.HoverAtRevision(context.Background(), uri, offset, revision)
		if err != nil || stale || !found {
			t.Fatalf("hover for %q = %#v, found=%v stale=%v err=%v", test.needle, hover, found, stale, err)
		}
		for _, expected := range test.contains {
			if !strings.Contains(hover.Contents, expected) {
				t.Errorf("hover for %q = %q, want %q", test.needle, hover.Contents, expected)
			}
		}
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
