package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/LehMichael/run-myscreens-lsp/internal/document"
	"github.com/LehMichael/run-myscreens-lsp/internal/model"
	"github.com/LehMichael/run-myscreens-lsp/internal/protocol"
	"github.com/LehMichael/run-myscreens-lsp/internal/syntax"
	"github.com/LehMichael/run-myscreens-lsp/internal/workspace"
)

const (
	serverName    = "run-myscreens-lsp"
	serverVersion = "0.1.0-dev"
)

type Server struct {
	connection  *protocol.Connection
	documents   *document.Store
	analyzer    syntax.Analyzer
	workspace   *workspace.Index
	logger      *log.Logger
	initialized bool
	shutdown    bool
	requestsMu  sync.Mutex
	requests    map[string]context.CancelFunc
	requestsWG  sync.WaitGroup
}

func New(connection *protocol.Connection, analyzer syntax.Analyzer, logger *log.Logger) *Server {
	return &Server{
		connection: connection,
		documents:  document.NewStore(),
		analyzer:   analyzer,
		workspace:  workspace.New(),
		logger:     logger,
		requests:   make(map[string]context.CancelFunc),
	}
}

func (s *Server) Run(ctx context.Context) error {
	for {
		message, err := s.connection.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.requestsWG.Wait()
				return nil
			}
			return err
		}
		if message.Method == "exit" {
			if !s.shutdown {
				return errors.New("received exit before shutdown")
			}
			s.requestsWG.Wait()
			return nil
		}
		if message.Method == "$/cancelRequest" {
			if err := s.cancelRequest(message.Params); err != nil && s.logger != nil {
				s.logger.Printf("%s: %v", message.Method, err)
			}
			continue
		}
		if s.isConcurrentRequest(message) {
			s.startRequest(ctx, message)
			continue
		}
		if err := s.handle(ctx, message); err != nil {
			if len(message.ID) > 0 {
				if replyErr := s.connection.ReplyError(message.ID, protocol.ResponseError{Code: protocol.ErrorCodeInternalError, Message: err.Error()}); replyErr != nil {
					return replyErr
				}
			} else if s.logger != nil {
				s.logger.Printf("%s: %v", message.Method, err)
			}
		}
	}
}

func (s *Server) isConcurrentRequest(message protocol.Message) bool {
	return s.initialized && !s.shutdown && len(message.ID) > 0 && (message.Method == "textDocument/references" || message.Method == "textDocument/completion")
}

func (s *Server) startRequest(parent context.Context, message protocol.Message) {
	ctx, cancel := context.WithCancel(parent)
	key := string(message.ID)
	s.requestsMu.Lock()
	s.requests[key] = cancel
	s.requestsMu.Unlock()
	s.requestsWG.Add(1)
	go func() {
		defer s.requestsWG.Done()
		defer cancel()
		defer func() {
			s.requestsMu.Lock()
			delete(s.requests, key)
			s.requestsMu.Unlock()
		}()
		if err := s.handle(ctx, message); err != nil {
			responseError := protocol.ResponseError{Code: protocol.ErrorCodeInternalError, Message: err.Error()}
			if errors.Is(err, context.Canceled) {
				responseError = protocol.ResponseError{Code: protocol.ErrorCodeRequestCanceled, Message: "request canceled"}
			}
			if replyErr := s.connection.ReplyError(message.ID, responseError); replyErr != nil && s.logger != nil {
				s.logger.Printf("%s: %v", message.Method, replyErr)
			}
		}
	}()
}

func (s *Server) cancelRequest(paramsJSON json.RawMessage) error {
	var params protocol.CancelParams
	if err := json.Unmarshal(paramsJSON, &params); err != nil {
		return fmt.Errorf("decode cancel params: %w", err)
	}
	s.requestsMu.Lock()
	cancel := s.requests[string(params.ID)]
	s.requestsMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *Server) cancelAllRequests() {
	s.requestsMu.Lock()
	defer s.requestsMu.Unlock()
	for _, cancel := range s.requests {
		cancel()
	}
}

func (s *Server) handle(ctx context.Context, message protocol.Message) error {
	if s.shutdown {
		if len(message.ID) > 0 {
			return s.connection.ReplyError(message.ID, protocol.ResponseError{Code: protocol.ErrorCodeInvalidRequest, Message: "server has shut down"})
		}
		return nil
	}
	if message.Method != "initialize" && !s.initialized {
		if len(message.ID) > 0 {
			return s.connection.ReplyError(message.ID, protocol.ResponseError{Code: protocol.ErrorCodeServerNotInit, Message: "server not initialized"})
		}
		return nil
	}

	switch message.Method {
	case "initialize":
		if s.initialized {
			return fmt.Errorf("server already initialized")
		}
		var params protocol.InitializeParams
		if len(message.Params) > 0 {
			if err := json.Unmarshal(message.Params, &params); err != nil {
				return fmt.Errorf("decode initialize params: %w", err)
			}
		}
		var rootURIs []string
		for _, folder := range params.WorkspaceFolders {
			rootURIs = append(rootURIs, folder.URI)
		}
		if len(rootURIs) == 0 && params.RootURI != "" {
			rootURIs = append(rootURIs, params.RootURI)
		}
		if err := s.workspace.Load(ctx, rootURIs, s.analyzer); err != nil {
			return err
		}
		s.initialized = true
		return s.connection.Reply(message.ID, protocol.InitializeResult{
			Capabilities: protocol.ServerCapabilities{
				PositionEncoding:       "utf-16",
				TextDocumentSync:       protocol.TextDocumentSyncOptions{OpenClose: true, Change: 1},
				DocumentSymbolProvider: true,
				FoldingRangeProvider:   true,
				DefinitionProvider:     true,
				ReferencesProvider:     true,
				CompletionProvider:     &protocol.CompletionOptions{},
			},
			ServerInfo: protocol.ServerInfo{Name: serverName, Version: serverVersion},
		})
	case "initialized":
		return nil
	case "shutdown":
		s.cancelAllRequests()
		s.requestsWG.Wait()
		s.shutdown = true
		return s.connection.Reply(message.ID, nil)
	case "textDocument/didOpen":
		var params protocol.DidOpenTextDocumentParams
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return fmt.Errorf("decode didOpen params: %w", err)
		}
		doc := s.documents.Open(params.TextDocument.URI, params.TextDocument.LanguageID, params.TextDocument.Version, params.TextDocument.Text)
		return s.publishDiagnostics(ctx, doc)
	case "textDocument/didChange":
		var params protocol.DidChangeTextDocumentParams
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return fmt.Errorf("decode didChange params: %w", err)
		}
		if len(params.ContentChanges) == 0 {
			return nil
		}
		change := params.ContentChanges[len(params.ContentChanges)-1]
		if change.Range != nil {
			return errors.New("incremental text changes are not supported; client must use full synchronization")
		}
		doc, ok := s.documents.Replace(params.TextDocument.URI, params.TextDocument.Version, change.Text)
		if !ok {
			return fmt.Errorf("document %q is not open", params.TextDocument.URI)
		}
		return s.publishDiagnostics(ctx, doc)
	case "textDocument/didClose":
		var params protocol.DidCloseTextDocumentParams
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return fmt.Errorf("decode didClose params: %w", err)
		}
		s.documents.Close(params.TextDocument.URI)
		if err := s.workspace.RemoveOverlay(ctx, params.TextDocument.URI, s.analyzer); err != nil {
			return err
		}
		return s.connection.Notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
			URI: params.TextDocument.URI, Diagnostics: []protocol.Diagnostic{},
		})
	case "textDocument/documentSymbol":
		var params protocol.DocumentSymbolParams
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return fmt.Errorf("decode documentSymbol params: %w", err)
		}
		doc, ok := s.documents.Get(params.TextDocument.URI)
		if !ok {
			return fmt.Errorf("document %q is not open", params.TextDocument.URI)
		}
		return s.connection.Reply(message.ID, documentSymbols(&doc, doc.Analysis.Symbols))
	case "textDocument/definition":
		var params protocol.TextDocumentPositionParams
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return fmt.Errorf("decode definition params: %w", err)
		}
		location, ok := s.definition(params)
		if !ok {
			return s.connection.Reply(message.ID, nil)
		}
		return s.connection.Reply(message.ID, location)
	case "textDocument/references":
		var params protocol.ReferenceParams
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return fmt.Errorf("decode references params: %w", err)
		}
		locations, err := s.references(ctx, params)
		if err != nil {
			return err
		}
		return s.connection.Reply(message.ID, locations)
	case "textDocument/completion":
		var params protocol.CompletionParams
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return fmt.Errorf("decode completion params: %w", err)
		}
		completion, err := s.completion(ctx, params)
		if err != nil {
			return err
		}
		return s.connection.Reply(message.ID, completion)
	case "textDocument/foldingRange":
		var params protocol.FoldingRangeParams
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return fmt.Errorf("decode foldingRange params: %w", err)
		}
		doc, ok := s.documents.Get(params.TextDocument.URI)
		if !ok {
			return fmt.Errorf("document %q is not open", params.TextDocument.URI)
		}
		return s.connection.Reply(message.ID, foldingRanges(&doc, doc.Analysis.Folds))
	default:
		if len(message.ID) > 0 {
			return s.connection.ReplyError(message.ID, protocol.ResponseError{Code: protocol.ErrorCodeMethodNotFound, Message: "method not found"})
		}
		return nil
	}
}

func (s *Server) publishDiagnostics(ctx context.Context, doc *document.Document) error {
	analysis, err := s.analyzer.Analyze(ctx, []byte(doc.Text))
	if err != nil {
		return err
	}
	if _, ok := s.documents.SetAnalysis(doc.URI, doc.Version, analysis); !ok {
		return fmt.Errorf("document %q changed while it was being analyzed", doc.URI)
	}
	doc.Analysis = analysis
	s.workspace.Overlay(doc.URI, doc.Text, analysis)
	items := make([]protocol.Diagnostic, 0, len(analysis.Diagnostics))
	for _, diagnostic := range analysis.Diagnostics {
		items = append(items, protocol.Diagnostic{
			Range:    doc.Range(diagnostic.Range.Start, diagnostic.Range.End),
			Severity: 1,
			Code:     diagnostic.Code,
			Source:   serverName,
			Message:  diagnostic.Message,
		})
	}
	version := doc.Version
	return s.connection.Notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
		URI: doc.URI, Version: &version, Diagnostics: items,
	})
}

func (s *Server) definition(params protocol.TextDocumentPositionParams) (*protocol.Location, bool) {
	doc, ok := s.documents.Get(params.TextDocument.URI)
	if !ok {
		indexed, indexedOK := s.workspace.Document(params.TextDocument.URI)
		if !indexedOK {
			return nil, false
		}
		doc = *document.New(indexed.URI, "run-myscreens", 0, indexed.Text)
		doc.Analysis = indexed.Analysis
	}
	offset, ok := doc.ByteOffsetAt(params.Position)
	if !ok {
		return nil, false
	}
	reference, ok := referenceAt(doc.Analysis.References, offset)
	if !ok {
		return nil, false
	}
	resolved, ok := s.workspace.Resolve(doc.URI, reference)
	if !ok {
		return nil, false
	}
	target := document.New(resolved.URI, "run-myscreens", 0, resolved.Text)
	return &protocol.Location{URI: resolved.URI, Range: target.Range(resolved.Range.Start, resolved.Range.End)}, true
}

func (s *Server) references(ctx context.Context, params protocol.ReferenceParams) ([]protocol.Location, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		indexed, revision, ok := s.workspace.DocumentSnapshot(params.TextDocument.URI)
		if !ok {
			return []protocol.Location{}, nil
		}
		doc := document.New(indexed.URI, "run-myscreens", 0, indexed.Text)
		offset, ok := doc.ByteOffsetAt(params.Position)
		if !ok {
			return []protocol.Location{}, nil
		}
		locations, found, stale, err := s.workspace.ReferencesAtRevision(ctx, indexed.URI, offset, params.Context.IncludeDeclaration, revision)
		if err != nil {
			return nil, err
		}
		if stale {
			continue
		}
		if !found {
			return []protocol.Location{}, nil
		}
		result := make([]protocol.Location, 0, len(locations))
		for _, location := range locations {
			target := document.New(location.URI, "run-myscreens", 0, location.Text)
			result = append(result, protocol.Location{URI: location.URI, Range: target.Range(location.Range.Start, location.Range.End)})
		}
		return result, nil
	}
	return []protocol.Location{}, nil
}

func (s *Server) completion(ctx context.Context, params protocol.CompletionParams) (protocol.CompletionList, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return protocol.CompletionList{}, err
		}
		indexed, revision, ok := s.workspace.DocumentSnapshot(params.TextDocument.URI)
		if !ok {
			return protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
		}
		doc := document.New(indexed.URI, "run-myscreens", 0, indexed.Text)
		offset, ok := doc.ByteOffsetAt(params.Position)
		if !ok {
			return protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
		}
		completionContext, err := s.analyzer.CompletionContext(ctx, []byte(indexed.Text), offset)
		if err != nil {
			return protocol.CompletionList{}, err
		}
		semantic, stale, err := s.workspace.CompletionsAtRevision(ctx, indexed.URI, completionContext, revision)
		if err != nil {
			return protocol.CompletionList{}, err
		}
		if stale {
			continue
		}
		items := completionItems(doc, completionContext, semantic)
		return protocol.CompletionList{Items: items}, nil
	}
	return protocol.CompletionList{IsIncomplete: true, Items: []protocol.CompletionItem{}}, nil
}

func completionItems(doc *document.Document, completionContext model.CompletionContext, semantic []model.CompletionItem) []protocol.CompletionItem {
	items := make(map[string]protocol.CompletionItem)
	replaceRange := doc.Range(completionContext.ReplaceRange.Start, completionContext.ReplaceRange.End)
	add := func(item protocol.CompletionItem) {
		if !strings.HasPrefix(strings.ToLower(item.Label), strings.ToLower(completionContext.Prefix)) {
			return
		}
		key := strings.ToLower(item.Label)
		if _, exists := items[key]; !exists {
			items[key] = item
		}
	}
	if completionContext.Kind == model.CompletionStatement {
		for _, keyword := range statementKeywords {
			add(completionProtocolItem(keyword, keyword, 14, "keyword", replaceRange))
		}
		for _, terminator := range completionContext.ExpectedTerminators {
			add(completionProtocolItem(terminator, terminator, 14, "expected terminator", replaceRange))
		}
	}
	for _, item := range semantic {
		insertText := item.InsertText
		if insertText == "" {
			insertText = item.Label
		}
		if completionContext.Quoted {
			insertText = strings.ReplaceAll(insertText, `"`, `""`)
		}
		add(completionProtocolItem(item.Label, insertText, lspCompletionKind(item.Kind), item.Detail, replaceRange))
	}
	result := make([]protocol.CompletionItem, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool {
		return strings.ToLower(result[left].Label) < strings.ToLower(result[right].Label)
	})
	return result
}

func completionProtocolItem(label, insertText string, kind int, detail string, replaceRange protocol.Range) protocol.CompletionItem {
	return protocol.CompletionItem{
		Label:      label,
		InsertText: insertText,
		Kind:       kind,
		Detail:     detail,
		TextEdit:   &protocol.TextEdit{Range: replaceRange, NewText: insertText},
	}
}

var statementKeywords = []string{
	"ACCESSLEVEL", "CALL", "CASE", "CHANNEL", "CHANGE", "CLEAR_BACKGROUND", "CONTROL", "DEFAULT", "DEF", "DO", "DO_UNTIL", "DO_WHILE",
	"EXIT", "FOCUS", "GC", "IF", "LA", "LANGUAGE", "LB", "LG", "LM", "LOAD", "LOOP_UNTIL", "LOOP_WHILE", "LS", "OUTPUT", "PRESS", "RESOLUTION", "RESUME", "RETURN",
	"SUB", "SUSPEND", "SWITCH", "UNLOAD",
}

func lspCompletionKind(kind model.CompletionItemKind) int {
	switch kind {
	case model.CompletionItemKeyword:
		return 14
	case model.CompletionItemVariable:
		return 6
	case model.CompletionItemFunction:
		return 3
	case model.CompletionItemMethod:
		return 2
	case model.CompletionItemModule:
		return 9
	case model.CompletionItemFile:
		return 17
	default:
		return 1
	}
}

func referenceAt(references []model.Reference, offset uint) (model.Reference, bool) {
	var best model.Reference
	found := false
	for _, reference := range references {
		if offset < reference.Range.Start || offset >= reference.Range.End {
			continue
		}
		if !found || reference.Range.End-reference.Range.Start < best.Range.End-best.Range.Start {
			best = reference
			found = true
		}
	}
	return best, found
}

func documentSymbols(doc *document.Document, symbols []model.Symbol) []protocol.DocumentSymbol {
	result := make([]protocol.DocumentSymbol, 0, len(symbols))
	for _, symbol := range symbols {
		result = append(result, protocol.DocumentSymbol{
			Name:           symbol.Name,
			Detail:         symbol.Detail,
			Kind:           lspSymbolKind(symbol.Kind),
			Range:          doc.Range(symbol.Range.Start, symbol.Range.End),
			SelectionRange: doc.Range(symbol.SelectionRange.Start, symbol.SelectionRange.End),
			Children:       documentSymbols(doc, symbol.Children),
		})
	}
	return result
}

func lspSymbolKind(kind model.SymbolKind) int {
	const (
		kindFile      = 1
		kindModule    = 2
		kindNamespace = 3
		kindMethod    = 6
		kindField     = 8
		kindFunction  = 12
		kindArray     = 18
		kindEvent     = 24
		kindStruct    = 23
	)
	switch kind {
	case model.SymbolDialog:
		return kindNamespace
	case model.SymbolSoftkeyMenu:
		return kindModule
	case model.SymbolArray:
		return kindArray
	case model.SymbolBlock, model.SymbolGrid:
		return kindStruct
	case model.SymbolVariable:
		return kindField
	case model.SymbolEvent:
		return kindEvent
	case model.SymbolSubprogram:
		return kindFunction
	case model.SymbolOutput:
		return kindMethod
	default:
		return kindFile
	}
}

func foldingRanges(doc *document.Document, folds []model.Fold) []protocol.FoldingRange {
	result := make([]protocol.FoldingRange, 0, len(folds))
	for _, fold := range folds {
		rangeValue := doc.Range(fold.Range.Start, fold.Range.End)
		endLine := rangeValue.End.Line
		endCharacter := rangeValue.End.Character
		if endCharacter == 0 && endLine > rangeValue.Start.Line {
			endLine--
		}
		if endLine <= rangeValue.Start.Line {
			continue
		}
		result = append(result, protocol.FoldingRange{
			StartLine:      rangeValue.Start.Line,
			StartCharacter: rangeValue.Start.Character,
			EndLine:        endLine,
			EndCharacter:   endCharacter,
			Kind:           string(fold.Kind),
		})
	}
	return result
}
