package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"

	"example.com/run-myscreens-lsp/internal/document"
	"example.com/run-myscreens-lsp/internal/model"
	"example.com/run-myscreens-lsp/internal/protocol"
	"example.com/run-myscreens-lsp/internal/syntax"
)

const (
	serverName    = "run-myscreens-lsp"
	serverVersion = "0.1.0-dev"
)

type Server struct {
	connection  *protocol.Connection
	documents   *document.Store
	analyzer    syntax.Analyzer
	logger      *log.Logger
	initialized bool
	shutdown    bool
}

func New(connection *protocol.Connection, analyzer syntax.Analyzer, logger *log.Logger) *Server {
	return &Server{
		connection: connection,
		documents:  document.NewStore(),
		analyzer:   analyzer,
		logger:     logger,
	}
}

func (s *Server) Run(ctx context.Context) error {
	for {
		message, err := s.connection.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if message.Method == "exit" {
			if !s.shutdown {
				return errors.New("received exit before shutdown")
			}
			return nil
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

func (s *Server) handle(ctx context.Context, message protocol.Message) error {
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
		s.initialized = true
		return s.connection.Reply(message.ID, protocol.InitializeResult{
			Capabilities: protocol.ServerCapabilities{
				PositionEncoding:       "utf-16",
				TextDocumentSync:       protocol.TextDocumentSyncOptions{OpenClose: true, Change: 1},
				DocumentSymbolProvider: true,
				FoldingRangeProvider:   true,
			},
			ServerInfo: protocol.ServerInfo{Name: serverName, Version: serverVersion},
		})
	case "initialized":
		return nil
	case "shutdown":
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
