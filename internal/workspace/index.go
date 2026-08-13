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
	URI   string
	Text  string
	Range model.ByteRange
}

type Index struct {
	mu        sync.RWMutex
	disk      map[string]Document
	overlays  map[string]Document
	paths     map[string][]string
	filenames map[string][]string
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
	i.overlays[canonicalURI] = Document{URI: canonicalURI, Path: path, Text: text, Analysis: analysis}
	i.rebuildPathsLocked()
}

func (i *Index) RemoveOverlay(ctx context.Context, uri string, analyzer syntax.Analyzer) error {
	canonicalURI, path := canonicalDocumentIdentity(uri)
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
	delete(i.overlays, canonicalURI)
	if diskExists {
		i.disk[diskDocument.URI] = diskDocument
	} else {
		delete(i.disk, canonicalURI)
	}
	i.rebuildPathsLocked()
	return nil
}

func (i *Index) Document(uri string) (Document, bool) {
	canonicalURI, _ := canonicalDocumentIdentity(uri)
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.documentLocked(canonicalURI)
}

func (i *Index) Resolve(uri string, reference model.Reference) (Location, bool) {
	canonicalURI, _ := canonicalDocumentIdentity(uri)
	i.mu.RLock()
	defer i.mu.RUnlock()

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
		return i.resolveInDocumentsLocked(i.loadedDocumentURIsLocked(source), reference)
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
				matches = append(matches, Location{URI: document.URI, Text: document.Text, Range: definition.SelectionRange})
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return Location{}, false
}

func (i *Index) loadedDocumentURIsLocked(source Document) []string {
	var uris []string
	for _, reference := range source.Analysis.References {
		if reference.Kind != model.DefinitionBlock || reference.TargetFile == "" {
			continue
		}
		matches := i.targetFileURIsLocked(source, reference.TargetFile)
		if len(matches) == 1 {
			uris = append(uris, matches[0])
		}
	}
	return uris
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
		return Location{URI: document.URI, Text: document.Text, Range: matches[0].SelectionRange}, resolutionFound
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
	for uri, document := range i.disk {
		if _, overlaid := i.overlays[uri]; overlaid {
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
