package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"

	"example.com/run-myscreens-lsp/internal/document"
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
				PositionEncoding: "utf-16",
				TextDocumentSync: protocol.TextDocumentSyncOptions{OpenClose: true, Change: 1},
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
	default:
		if len(message.ID) > 0 {
			return s.connection.ReplyError(message.ID, protocol.ResponseError{Code: protocol.ErrorCodeMethodNotFound, Message: "method not found"})
		}
		return nil
	}
}

func (s *Server) publishDiagnostics(ctx context.Context, doc *document.Document) error {
	diagnostics, err := s.analyzer.Analyze(ctx, []byte(doc.Text))
	if err != nil {
		return err
	}
	items := make([]protocol.Diagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		items = append(items, protocol.Diagnostic{
			Range:    doc.Range(diagnostic.StartByte, diagnostic.EndByte),
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
