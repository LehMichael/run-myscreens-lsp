package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/LehMichael/run-myscreens-lsp/internal/model"
	"github.com/LehMichael/run-myscreens-lsp/internal/syntax"
)

type Document struct {
	URI      string
	Path     string
	Text     string
	Analysis model.Analysis
}

type Location struct {
	URI             string
	Text            string
	Range           model.ByteRange
	DefinitionRange model.ByteRange
}

type Index struct {
	mu        sync.RWMutex
	disk      map[string]Document
	overlays  map[string]Document
	paths     map[string][]string
	filenames map[string][]string
	revision  uint64
}

func New() *Index {
	return &Index{
		disk:      make(map[string]Document),
		overlays:  make(map[string]Document),
		paths:     make(map[string][]string),
		filenames: make(map[string][]string),
	}
}

func (i *Index) Load(ctx context.Context, rootURIs []string, analyzer syntax.Analyzer) error {
	disk := make(map[string]Document)
	for _, rootURI := range rootURIs {
		if rootURI == "" {
			continue
		}
		rootPath, err := PathFromFileURI(rootURI)
		if err != nil {
			return fmt.Errorf("decode workspace root: %w", err)
		}
		info, err := os.Stat(rootPath)
		if err != nil {
			return fmt.Errorf("stat workspace root: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("workspace root %q is not a directory", rootPath)
		}
		if err := scanRoot(ctx, rootPath, analyzer, disk); err != nil {
			return err
		}
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.disk = disk
	i.revision++
	i.rebuildPathsLocked()
	return nil
}

func scanRoot(ctx context.Context, rootPath string, analyzer syntax.Analyzer, disk map[string]Document) error {
	return filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".com") {
			return nil
		}
		document, err := analyzeFile(ctx, path, analyzer)
		if err != nil {
			return err
		}
		disk[document.URI] = document
		return nil
	})
}

func analyzeFile(ctx context.Context, path string, analyzer syntax.Analyzer) (Document, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("read %q: %w", path, err)
	}
	analysis, err := analyzer.Analyze(ctx, contents)
	if err != nil {
		return Document{}, fmt.Errorf("analyze %q: %w", path, err)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return Document{}, fmt.Errorf("make %q absolute: %w", path, err)
	}
	uri := FileURI(absolutePath)
	return Document{URI: uri, Path: filepath.Clean(absolutePath), Text: string(contents), Analysis: analysis}, nil
}

func (i *Index) Overlay(uri, text string, analysis model.Analysis) {
	canonicalURI, path := canonicalDocumentIdentity(uri)
	i.mu.Lock()
	defer i.mu.Unlock()
	canonicalURI = i.uriForPathLocked(canonicalURI, path)
	if existing, ok := i.documentLocked(canonicalURI); ok && existing.Path != "" {
		path = existing.Path
	}
	i.overlays[canonicalURI] = Document{URI: canonicalURI, Path: path, Text: text, Analysis: analysis}
	i.revision++
	i.rebuildPathsLocked()
}

func (i *Index) RemoveOverlay(ctx context.Context, uri string, analyzer syntax.Analyzer) error {
	canonicalURI, path := canonicalDocumentIdentity(uri)
	i.mu.RLock()
	canonicalURI = i.uriForPathLocked(canonicalURI, path)
	if existing, ok := i.documentLocked(canonicalURI); ok && existing.Path != "" {
		path = existing.Path
	}
	i.mu.RUnlock()
	var diskDocument Document
	var diskExists bool
	if path != "" && strings.EqualFold(filepath.Ext(path), ".com") {
		if _, err := os.Stat(path); err == nil {
			var analyzeErr error
			diskDocument, analyzeErr = analyzeFile(ctx, path, analyzer)
			if analyzeErr != nil {
				return analyzeErr
			}
			diskExists = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %q: %w", path, err)
		}
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	canonicalURI = i.uriForPathLocked(canonicalURI, path)
	delete(i.overlays, canonicalURI)
	if diskExists {
		i.disk[diskDocument.URI] = diskDocument
	} else {
		delete(i.disk, canonicalURI)
	}
	i.revision++
	i.rebuildPathsLocked()
	return nil
}

func (i *Index) Document(uri string) (Document, bool) {
	document, _, ok := i.DocumentSnapshot(uri)
	return document, ok
}

func (i *Index) DocumentSnapshot(uri string) (Document, uint64, bool) {
	canonicalURI, path := canonicalDocumentIdentity(uri)
	i.mu.RLock()
	defer i.mu.RUnlock()
	document, ok := i.documentLocked(i.uriForPathLocked(canonicalURI, path))
	return document, i.revision, ok
}

func (i *Index) Resolve(uri string, reference model.Reference) (Location, bool) {
	canonicalURI, path := canonicalDocumentIdentity(uri)
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.resolveLocked(i.uriForPathLocked(canonicalURI, path), reference)
}

func (i *Index) References(ctx context.Context, uri string, offset uint, includeDeclaration bool) ([]Location, bool, error) {
	return i.references(ctx, uri, offset, includeDeclaration, 0, false)
}

func (i *Index) ReferencesAtRevision(ctx context.Context, uri string, offset uint, includeDeclaration bool, revision uint64) ([]Location, bool, bool, error) {
	locations, found, err := i.references(ctx, uri, offset, includeDeclaration, revision, true)
	if errors.Is(err, errStaleRevision) {
		return nil, false, true, nil
	}
	return locations, found, false, err
}

var errStaleRevision = errors.New("workspace revision changed")

func (i *Index) references(ctx context.Context, uri string, offset uint, includeDeclaration bool, revision uint64, requireRevision bool) ([]Location, bool, error) {
	canonicalURI, path := canonicalDocumentIdentity(uri)
	i.mu.RLock()
	if requireRevision && revision != i.revision {
		i.mu.RUnlock()
		return nil, false, errStaleRevision
	}
	canonicalURI = i.uriForPathLocked(canonicalURI, path)
	snapshot := i.snapshotLocked()
	i.mu.RUnlock()

	source, ok := snapshot.documentLocked(canonicalURI)
	if !ok {
		return nil, false, nil
	}
	target, ok := snapshot.targetAtLocked(source, offset)
	if !ok {
		return nil, false, nil
	}

	var locations []Location
	if includeDeclaration {
		locations = append(locations, target)
	}
	for _, candidate := range snapshot.documentsLocked() {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		for _, reference := range candidate.Analysis.References {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			resolved, ok := snapshot.resolveLocked(candidate.URI, reference)
			if ok && sameLocation(resolved, target) {
				locations = append(locations, Location{URI: candidate.URI, Text: candidate.Text, Range: reference.Range})
			}
		}
	}
	sort.Slice(locations, func(left, right int) bool {
		if locations[left].URI != locations[right].URI {
			return locations[left].URI < locations[right].URI
		}
		if locations[left].Range.Start != locations[right].Range.Start {
			return locations[left].Range.Start < locations[right].Range.Start
		}
		return locations[left].Range.End < locations[right].Range.End
	})
	return locations, true, nil
}

func (i *Index) targetAtLocked(source Document, offset uint) (Location, bool) {
	var target Location
	found := false
	bestLength := ^uint(0)
	for _, definition := range source.Analysis.Definitions {
		if !rangeContains(definition.SelectionRange, offset) {
			continue
		}
		length := definition.SelectionRange.End - definition.SelectionRange.Start
		if length < bestLength {
			target = Location{URI: source.URI, Text: source.Text, Range: definition.SelectionRange, DefinitionRange: definition.Range}
			bestLength = length
			found = true
		}
	}
	for _, reference := range source.Analysis.References {
		if !rangeContains(reference.Range, offset) {
			continue
		}
		length := reference.Range.End - reference.Range.Start
		if length >= bestLength {
			continue
		}
		resolved, ok := i.resolveLocked(source.URI, reference)
		if !ok {
			continue
		}
		target = resolved
		bestLength = length
		found = true
	}
	return target, found
}

func (i *Index) documentsLocked() []Document {
	documents := make([]Document, 0, len(i.disk)+len(i.overlays))
	for uri, document := range i.disk {
		if _, overlaid := i.overlays[uri]; !overlaid {
			documents = append(documents, document)
		}
	}
	for _, document := range i.overlays {
		documents = append(documents, document)
	}
	sort.Slice(documents, func(left, right int) bool { return documents[left].URI < documents[right].URI })
	return documents
}

func sameLocation(left, right Location) bool {
	return left.URI == right.URI && left.Range == right.Range
}

func rangeContains(byteRange model.ByteRange, offset uint) bool {
	return byteRange.Start <= offset && offset < byteRange.End
}

func (i *Index) resolveLocked(canonicalURI string, reference model.Reference) (Location, bool) {
	source, ok := i.documentLocked(canonicalURI)
	if !ok {
		return Location{}, false
	}
	if reference.TargetFile != "" {
		candidateURIs := i.targetFileURIsLocked(source, reference.TargetFile)
		if len(candidateURIs) != 1 {
			return Location{}, false
		}
		return i.resolveInDocumentsLocked(candidateURIs, reference)
	}
	location, scopedResult := resolveScoped(source, reference)
	if scopedResult == resolutionFound {
		return location, true
	}
	if scopedResult == resolutionAmbiguous {
		return Location{}, false
	}
	if reference.Kind == model.DefinitionVariable || reference.Kind == model.DefinitionArrayOrGrid {
		return Location{}, false
	}
	if reference.Kind == model.DefinitionSubprogram || reference.Kind == model.DefinitionOutput {
		return i.resolveInLoadedBlocksLocked(source, reference)
	}
	return Location{}, false
}

func (i *Index) resolveInDocumentsLocked(candidateURIs []string, reference model.Reference) (Location, bool) {
	seen := make(map[string]bool)
	var matches []Location
	for _, candidateURI := range candidateURIs {
		if seen[candidateURI] {
			continue
		}
		seen[candidateURI] = true
		document, ok := i.documentLocked(candidateURI)
		if !ok {
			continue
		}
		for _, definition := range document.Analysis.Definitions {
			if definitionMatches(reference, definition) {
				matches = append(matches, Location{URI: document.URI, Text: document.Text, Range: definition.SelectionRange, DefinitionRange: definition.Range})
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return Location{}, false
}

func (i *Index) resolveInLoadedBlocksLocked(source Document, call model.Reference) (Location, bool) {
	unique := make(map[string]Location)
	for _, load := range source.Analysis.References {
		if load.Kind != model.DefinitionBlock || load.TargetFile == "" || !sameOwningEntity(load.Scope, call.Scope) {
			continue
		}
		block, ok := i.resolveLocked(source.URI, load)
		if !ok {
			continue
		}
		document, ok := i.documentLocked(block.URI)
		if !ok {
			continue
		}
		for _, definition := range document.Analysis.Definitions {
			if definitionMatches(call, definition) && scopeContains(definition.Scope, block.DefinitionRange) {
				location := Location{URI: document.URI, Text: document.Text, Range: definition.SelectionRange, DefinitionRange: definition.Range}
				unique[locationKey(location)] = location
			}
		}
	}
	if len(unique) == 1 {
		for _, location := range unique {
			return location, true
		}
	}
	return Location{}, false
}

func sameOwningEntity(left, right []model.ByteRange) bool {
	return len(left) > 0 && len(right) > 0 && left[0] == right[0]
}

func locationKey(location Location) string {
	return fmt.Sprintf("%s:%d:%d", location.URI, location.Range.Start, location.Range.End)
}

func scopeContains(scope []model.ByteRange, owner model.ByteRange) bool {
	for _, candidate := range scope {
		if candidate == owner {
			return true
		}
	}
	return false
}

type resolutionResult uint8

const (
	resolutionAbsent resolutionResult = iota
	resolutionFound
	resolutionAmbiguous
)

func resolveScoped(document Document, reference model.Reference) (Location, resolutionResult) {
	if reference.Kind == model.DefinitionArrayOrGrid {
		variableReference := reference
		variableReference.Kind = model.DefinitionVariable
		location, result := resolveScoped(document, variableReference)
		if result != resolutionAbsent {
			return location, result
		}
	}
	bestDepth := -1
	var matches []model.Definition
	for _, definition := range document.Analysis.Definitions {
		if !definitionMatches(reference, definition) || !visibleFrom(reference, definition) {
			continue
		}
		depth := commonScopeDepth(reference.Scope, definition.Scope)
		if depth > bestDepth {
			bestDepth = depth
			matches = matches[:0]
		}
		if depth == bestDepth {
			matches = append(matches, definition)
		}
	}
	switch len(matches) {
	case 0:
		return Location{}, resolutionAbsent
	case 1:
		return Location{URI: document.URI, Text: document.Text, Range: matches[0].SelectionRange, DefinitionRange: matches[0].Range}, resolutionFound
	default:
		return Location{}, resolutionAmbiguous
	}
}

func visibleFrom(reference model.Reference, definition model.Definition) bool {
	if definition.Kind == model.DefinitionVariable {
		if len(definition.Scope) == 0 || len(reference.Scope) < len(definition.Scope) {
			return false
		}
		for index := range definition.Scope {
			if definition.Scope[index] != reference.Scope[index] {
				return false
			}
		}
	}
	return true
}

func commonScopeDepth(left, right []model.ByteRange) int {
	depth := 0
	for depth < len(left) && depth < len(right) && left[depth] == right[depth] {
		depth++
	}
	return depth
}

func definitionMatches(reference model.Reference, definition model.Definition) bool {
	if !strings.EqualFold(reference.Name, definition.Name) {
		return false
	}
	if reference.Kind == model.DefinitionArrayOrGrid {
		return definition.Kind == model.DefinitionArray || definition.Kind == model.DefinitionGrid
	}
	return reference.Kind == definition.Kind
}

func (i *Index) targetFileURIsLocked(source Document, targetFile string) []string {
	cleanTarget := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(targetFile, "\\", "/")))
	if filepath.IsAbs(cleanTarget) {
		return append([]string(nil), i.paths[canonicalPath(cleanTarget)]...)
	}
	if source.Path != "" {
		relativeKey := canonicalPath(filepath.Join(filepath.Dir(source.Path), cleanTarget))
		if matches := i.paths[relativeKey]; len(matches) > 0 {
			return append([]string(nil), matches...)
		}
	}
	if filepath.Base(cleanTarget) != cleanTarget {
		return nil
	}
	return append([]string(nil), i.filenames[canonicalFilename(cleanTarget)]...)
}

func (i *Index) snapshotLocked() *Index {
	return &Index{
		disk:      cloneDocuments(i.disk),
		overlays:  cloneDocuments(i.overlays),
		paths:     cloneStringSlices(i.paths),
		filenames: cloneStringSlices(i.filenames),
		revision:  i.revision,
	}
}

func cloneDocuments(source map[string]Document) map[string]Document {
	result := make(map[string]Document, len(source))
	for key, document := range source {
		result[key] = document
	}
	return result
}

func cloneStringSlices(source map[string][]string) map[string][]string {
	result := make(map[string][]string, len(source))
	for key, values := range source {
		result[key] = append([]string(nil), values...)
	}
	return result
}

func (i *Index) uriForPathLocked(fallbackURI, path string) string {
	if path == "" {
		return fallbackURI
	}
	matches := i.paths[canonicalPath(path)]
	if len(matches) == 1 {
		return matches[0]
	}
	return fallbackURI
}

func (i *Index) documentLocked(uri string) (Document, bool) {
	if document, ok := i.overlays[uri]; ok {
		return document, true
	}
	document, ok := i.disk[uri]
	return document, ok
}

func (i *Index) rebuildPathsLocked() {
	i.paths = make(map[string][]string)
	i.filenames = make(map[string][]string)
	seen := make(map[string]bool)
	overlaidPaths := make(map[string]bool, len(i.overlays))
	for _, document := range i.overlays {
		if document.Path != "" {
			overlaidPaths[canonicalPath(document.Path)] = true
		}
	}
	for uri, document := range i.disk {
		if _, overlaid := i.overlays[uri]; overlaid || overlaidPaths[canonicalPath(document.Path)] {
			continue
		}
		i.addPathLocked(uri, document.Path, seen)
	}
	for uri, document := range i.overlays {
		i.addPathLocked(uri, document.Path, seen)
	}
	for key := range i.paths {
		sort.Strings(i.paths[key])
	}
	for key := range i.filenames {
		sort.Strings(i.filenames[key])
	}
}

func (i *Index) addPathLocked(uri, path string, seen map[string]bool) {
	if seen[uri] {
		return
	}
	seen[uri] = true
	if path == "" {
		return
	}
	i.paths[canonicalPath(path)] = append(i.paths[canonicalPath(path)], uri)
	name := filepath.Base(path)
	i.filenames[canonicalFilename(name)] = append(i.filenames[canonicalFilename(name)], uri)
}

func canonicalDocumentIdentity(uri string) (string, string) {
	path, err := PathFromFileURI(uri)
	if err != nil {
		return uri, ""
	}
	absolutePath, err := filepath.Abs(path)
	if err == nil {
		path = absolutePath
	}
	path = filepath.Clean(path)
	return FileURI(path), path
}

func canonicalPath(path string) string {
	absolutePath, err := filepath.Abs(path)
	if err == nil {
		path = absolutePath
	}
	return strings.ToLower(filepath.Clean(path))
}

func canonicalFilename(name string) string {
	return strings.ToLower(filepath.Clean(name))
}

func PathFromFileURI(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("unsupported URI scheme %q", parsed.Scheme)
	}
	path, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" && parsed.Host != "" && parsed.Host != "localhost" {
		return filepath.FromSlash("//" + parsed.Host + path), nil
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "", fmt.Errorf("unsupported file URI host %q", parsed.Host)
	}
	if path == "" {
		return "", errors.New("file URI has no path")
	}
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path), nil
}

func FileURI(path string) string {
	absolutePath, err := filepath.Abs(path)
	if err == nil {
		path = absolutePath
	}
	slashPath := filepath.ToSlash(filepath.Clean(path))
	if runtime.GOOS == "windows" && strings.HasPrefix(slashPath, "//") {
		parts := strings.SplitN(strings.TrimPrefix(slashPath, "//"), "/", 2)
		uri := &url.URL{Scheme: "file", Host: parts[0]}
		if len(parts) == 2 {
			uri.Path = "/" + parts[1]
		}
		return uri.String()
	}
	if runtime.GOOS == "windows" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}
