package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/LehMichael/run-myscreens-lsp/internal/protocol"
	"github.com/LehMichael/run-myscreens-lsp/internal/syntax"
	"github.com/LehMichael/run-myscreens-lsp/internal/workspace"
)

func TestProtocolLifecyclePublishesDiagnosticsSymbolsAndFolds(t *testing.T) {
	uri := "file:///workspace/test.com"
	validSource := "; 😀\n//M(Main)\nDEF value(1)=(I/0/1)\nLOAD\n  IF value==1\n    CALL(\"Update\")\n  ENDIF\nEND_LOAD\nSUB(Update)\n  RETURN\nEND_SUB\n//END\n"
	input := bytes.NewBufferString(
		frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`) +
			frame(`{"jsonrpc":"2.0","method":"initialized","params":{}}`) +
			frame(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"`+uri+`","languageId":"run-myscreens","version":1,"text":"//M(Test)\nLOAD\nEND_PRESS\n//END\n"}}}`) +
			frameJSON(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didChange", "params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 2}, "contentChanges": []map[string]string{{"text": validSource}}}}) +
			frame(`{"jsonrpc":"2.0","id":3,"method":"textDocument/documentSymbol","params":{"textDocument":{"uri":"`+uri+`"}}}`) +
			frame(`{"jsonrpc":"2.0","id":4,"method":"textDocument/foldingRange","params":{"textDocument":{"uri":"`+uri+`"}}}`) +
			frame(`{"jsonrpc":"2.0","method":"textDocument/didClose","params":{"textDocument":{"uri":"`+uri+`"}}}`) +
			frame(`{"jsonrpc":"2.0","id":2,"method":"shutdown"}`) +
			frame(`{"jsonrpc":"2.0","method":"exit"}`),
	)
	var output bytes.Buffer
	connection := protocol.NewConnection(input, &output)
	languageServer := New(connection, syntax.NewTreeSitterAnalyzer(), log.New(io.Discard, "", 0))
	if err := languageServer.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	messages := readMessages(t, &output)
	if len(messages) != 7 {
		t.Fatalf("message count = %d, want 7: %#v", len(messages), messages)
	}

	var initialize protocol.InitializeResult
	if err := json.Unmarshal(messages[0].Result, &initialize); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	capabilities := initialize.Capabilities
	if capabilities.PositionEncoding != "utf-16" || capabilities.TextDocumentSync.Change != 1 ||
		!capabilities.DocumentSymbolProvider || !capabilities.FoldingRangeProvider {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}

	openDiagnostics := decodeDiagnostics(t, messages[1])
	if len(openDiagnostics.Diagnostics) != 1 || openDiagnostics.Diagnostics[0].Code != "mismatched-event-end" {
		t.Fatalf("open diagnostics = %#v", openDiagnostics.Diagnostics)
	}
	changeDiagnostics := decodeDiagnostics(t, messages[2])
	if len(changeDiagnostics.Diagnostics) != 0 || changeDiagnostics.Version == nil || *changeDiagnostics.Version != 2 {
		t.Fatalf("change diagnostics = %#v", changeDiagnostics)
	}

	var symbols []protocol.DocumentSymbol
	if err := json.Unmarshal(messages[3].Result, &symbols); err != nil {
		t.Fatalf("decode symbols: %v", err)
	}
	if len(symbols) != 1 || symbols[0].Name != "Main" || len(symbols[0].Children) != 3 {
		t.Fatalf("symbols = %#v", symbols)
	}
	if got := symbols[0].SelectionRange.Start; got != (protocol.Position{Line: 1, Character: 4}) {
		t.Fatalf("dialog selection start = %#v, want line 1 character 4", got)
	}
	if symbols[0].Children[0].Name != "value" || symbols[0].Children[0].Detail != "variable (1)" {
		t.Fatalf("versioned declaration symbol = %#v", symbols[0].Children[0])
	}

	var folds []protocol.FoldingRange
	if err := json.Unmarshal(messages[4].Result, &folds); err != nil {
		t.Fatalf("decode folds: %v", err)
	}
	if len(folds) != 4 {
		t.Fatalf("folds = %#v, want dialog, event, if, subprogram", folds)
	}
	if folds[0].StartLine != 1 || folds[0].EndLine != 11 {
		t.Fatalf("dialog fold = %#v", folds[0])
	}

	closeDiagnostics := decodeDiagnostics(t, messages[5])
	if len(closeDiagnostics.Diagnostics) != 0 || closeDiagnostics.Version != nil {
		t.Fatalf("close diagnostics = %#v", closeDiagnostics)
	}
	if string(messages[6].ID) != "2" || string(messages[6].Result) != "null" {
		t.Fatalf("shutdown response = %#v", messages[6])
	}
}

func TestDefinitionResolvesSameAndCrossFileWithUTF16Ranges(t *testing.T) {
	root := t.TempDir()
	targetPath := filepath.Join(root, "Shared.com")
	if err := os.WriteFile(targetPath, []byte("; 😀\n//M(Target)\n//END\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	uri := workspace.FileURI(filepath.Join(root, "Main.com"))
	source := "; 😀\n//M(Main)\nDEF value=(I/0/1)\nLOAD\n  value=\"😀\" << CALL(\"local\")\n  LM(\"TARGET\", \"shared.COM\")\nEND_LOAD\nSUB(Local)\nEND_SUB\n//END\n"
	input := bytes.NewBufferString(
		frameJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"rootUri": workspace.FileURI(root), "capabilities": map[string]any{}}}) +
			frameJSON(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "run-myscreens", "version": 1, "text": source}}}) +
			frameJSON(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition", "params": map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]any{"line": 4, "character": 23}}}) +
			frameJSON(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "textDocument/definition", "params": map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]any{"line": 5, "character": 7}}}) +
			frameJSON(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "textDocument/definition", "params": map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]any{"line": 4, "character": 3}}}) +
			frame(`{"jsonrpc":"2.0","id":5,"method":"shutdown"}`) +
			frame(`{"jsonrpc":"2.0","method":"exit"}`),
	)
	var output bytes.Buffer
	languageServer := New(protocol.NewConnection(input, &output), syntax.NewTreeSitterAnalyzer(), log.New(io.Discard, "", 0))
	if err := languageServer.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	messages := readMessages(t, &output)
	if len(messages) != 6 {
		t.Fatalf("message count = %d, want 6: %#v", len(messages), messages)
	}
	var initialize protocol.InitializeResult
	if err := json.Unmarshal(messages[0].Result, &initialize); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}
	if !initialize.Capabilities.DefinitionProvider {
		t.Fatal("definition provider was not advertised")
	}
	var sameFile protocol.Location
	if err := json.Unmarshal(messages[2].Result, &sameFile); err != nil {
		t.Fatalf("decode same-file location: %v", err)
	}
	if sameFile.URI != uri || sameFile.Range.Start != (protocol.Position{Line: 7, Character: 4}) || sameFile.Range.End != (protocol.Position{Line: 7, Character: 9}) {
		t.Fatalf("same-file location = %#v", sameFile)
	}
	var crossFile protocol.Location
	if err := json.Unmarshal(messages[3].Result, &crossFile); err != nil {
		t.Fatalf("decode cross-file location: %v", err)
	}
	if crossFile.URI != workspace.FileURI(targetPath) || crossFile.Range.Start != (protocol.Position{Line: 1, Character: 4}) || crossFile.Range.End != (protocol.Position{Line: 1, Character: 10}) {
		t.Fatalf("cross-file location = %#v", crossFile)
	}
	var variable protocol.Location
	if err := json.Unmarshal(messages[4].Result, &variable); err != nil {
		t.Fatalf("decode variable location: %v", err)
	}
	if variable.URI != uri || variable.Range.Start != (protocol.Position{Line: 2, Character: 4}) || variable.Range.End != (protocol.Position{Line: 2, Character: 9}) {
		t.Fatalf("variable location = %#v", variable)
	}
}

func TestReferencesResolveCrossFileWithUTF16Ranges(t *testing.T) {
	root := t.TempDir()
	targetPath := filepath.Join(root, "Shared.com")
	if err := os.WriteFile(targetPath, []byte("; 😀\n//M(Target)\n//END\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	otherPath := filepath.Join(root, "Other.com")
	if err := os.WriteFile(otherPath, []byte("//M(Other)\nLOAD\n  LM(\"target\", \"SHARED.com\")\nEND_LOAD\n//END\n"), 0o600); err != nil {
		t.Fatalf("write other: %v", err)
	}
	uri := workspace.FileURI(filepath.Join(root, "Main.com"))
	source := "//M(Main)\nLOAD\n  value=\"😀\" << LM(\"TARGET\", \"shared.COM\")\nEND_LOAD\n//END\n"
	input := bytes.NewBufferString(
		frameJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"rootUri": workspace.FileURI(root), "capabilities": map[string]any{}}}) +
			frameJSON(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "run-myscreens", "version": 1, "text": source}}}) +
			frameJSON(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/references", "params": map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]any{"line": 2, "character": 20}, "context": map[string]any{"includeDeclaration": true}}}) +
			frameJSON(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "textDocument/references", "params": map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]any{"line": 0, "character": 0}, "context": map[string]any{"includeDeclaration": false}}}),
	)
	var output bytes.Buffer
	languageServer := New(protocol.NewConnection(input, &output), syntax.NewTreeSitterAnalyzer(), log.New(io.Discard, "", 0))
	if err := languageServer.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	messages := readMessages(t, &output)
	if len(messages) != 4 {
		t.Fatalf("message count = %d, want 4: %#v", len(messages), messages)
	}
	initializeMessage := messageByID(t, messages, "1")
	var initialize protocol.InitializeResult
	if err := json.Unmarshal(initializeMessage.Result, &initialize); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}
	if !initialize.Capabilities.ReferencesProvider {
		t.Fatal("references provider was not advertised")
	}
	var locations []protocol.Location
	if err := json.Unmarshal(messageByID(t, messages, "2").Result, &locations); err != nil {
		t.Fatalf("decode references: %v", err)
	}
	if len(locations) != 3 {
		t.Fatalf("references = %#v", locations)
	}
	if locations[0].URI != uri || locations[0].Range.Start != (protocol.Position{Line: 2, Character: 19}) || locations[0].Range.End != (protocol.Position{Line: 2, Character: 27}) {
		t.Fatalf("open-buffer UTF-16 reference = %#v", locations[0])
	}
	if locations[1].URI != workspace.FileURI(otherPath) || locations[1].Range.Start != (protocol.Position{Line: 2, Character: 5}) {
		t.Fatalf("other-file reference = %#v", locations[1])
	}
	if locations[2].URI != workspace.FileURI(targetPath) || locations[2].Range.Start != (protocol.Position{Line: 1, Character: 4}) {
		t.Fatalf("declaration = %#v", locations[2])
	}
	var empty []protocol.Location
	if err := json.Unmarshal(messageByID(t, messages, "3").Result, &empty); err != nil {
		t.Fatalf("decode empty references: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty references = %#v", empty)
	}
}

func TestReferencesConcurrencyRequiresInitializedActiveServer(t *testing.T) {
	languageServer := New(protocol.NewConnection(bytes.NewReader(nil), io.Discard), syntax.NewTreeSitterAnalyzer(), log.New(io.Discard, "", 0))
	message := protocol.Message{ID: json.RawMessage(`1`), Method: "textDocument/references"}
	if languageServer.isConcurrentRequest(message) {
		t.Fatal("references dispatched concurrently before initialization")
	}
	languageServer.initialized = true
	if !languageServer.isConcurrentRequest(message) {
		t.Fatal("references was not concurrent after initialization")
	}
	languageServer.shutdown = true
	if languageServer.isConcurrentRequest(message) {
		t.Fatal("references dispatched concurrently after shutdown")
	}
}

func TestRequestAfterShutdownReturnsInvalidRequest(t *testing.T) {
	var output bytes.Buffer
	languageServer := New(protocol.NewConnection(bytes.NewReader(nil), &output), syntax.NewTreeSitterAnalyzer(), log.New(io.Discard, "", 0))
	languageServer.initialized = true
	languageServer.shutdown = true
	message := protocol.Message{JSONRPC: "2.0", ID: json.RawMessage(`9`), Method: "textDocument/references", Params: json.RawMessage(`{}`)}
	if err := languageServer.handle(context.Background(), message); err != nil {
		t.Fatalf("handle: %v", err)
	}
	responses := readMessages(t, &output)
	if len(responses) != 1 || responses[0].Error == nil || responses[0].Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("post-shutdown response = %#v", responses)
	}
}

func TestCancelRequestCancelsRegisteredRequest(t *testing.T) {
	languageServer := New(protocol.NewConnection(bytes.NewReader(nil), io.Discard), syntax.NewTreeSitterAnalyzer(), log.New(io.Discard, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	languageServer.requests["42"] = cancel
	if err := languageServer.cancelRequest(json.RawMessage(`{"id":42}`)); err != nil {
		t.Fatalf("cancelRequest: %v", err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("registered request was not canceled")
	}
}

func TestCanceledConcurrentRequestReturnsRequestCanceled(t *testing.T) {
	var output bytes.Buffer
	languageServer := New(protocol.NewConnection(bytes.NewReader(nil), &output), syntax.NewTreeSitterAnalyzer(), log.New(io.Discard, "", 0))
	languageServer.initialized = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	message := protocol.Message{JSONRPC: "2.0", ID: json.RawMessage(`7`), Method: "textDocument/references", Params: json.RawMessage(`{"textDocument":{"uri":"file:///missing.com"},"position":{"line":0,"character":0},"context":{"includeDeclaration":false}}`)}
	languageServer.startRequest(ctx, message)
	languageServer.requestsWG.Wait()
	responses := readMessages(t, &output)
	if len(responses) != 1 || responses[0].Error == nil || responses[0].Error.Code != protocol.ErrorCodeRequestCanceled {
		t.Fatalf("canceled response = %#v", responses)
	}
}

func TestPublishedDiagnosticRangeUsesUTF16(t *testing.T) {
	uri := "file:///workspace/unicode.com"
	source := "//M(Test)\nDEF value={ST=\"😀\", BC=}\n//END\n"
	input := bytes.NewBufferString(
		frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`) +
			frameJSON(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "run-myscreens", "version": 1, "text": source}}}) +
			frame(`{"jsonrpc":"2.0","id":2,"method":"shutdown"}`) +
			frame(`{"jsonrpc":"2.0","method":"exit"}`),
	)
	var output bytes.Buffer
	languageServer := New(protocol.NewConnection(input, &output), syntax.NewTreeSitterAnalyzer(), log.New(io.Discard, "", 0))
	if err := languageServer.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	messages := readMessages(t, &output)
	if len(messages) != 3 {
		t.Fatalf("message count = %d, want 3", len(messages))
	}
	diagnostics := decodeDiagnostics(t, messages[1]).Diagnostics
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one", diagnostics)
	}
	if got, want := diagnostics[0].Range.Start, (protocol.Position{Line: 1, Character: 22}); got != want {
		t.Fatalf("diagnostic start = %#v, want %#v", got, want)
	}
}

func readMessages(t *testing.T, output io.Reader) []protocol.Message {
	t.Helper()
	reader := protocol.NewConnection(output, io.Discard)
	var messages []protocol.Message
	for {
		message, err := reader.Read()
		if err == io.EOF {
			return messages
		}
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		messages = append(messages, message)
	}
}

func messageByID(t *testing.T, messages []protocol.Message, id string) protocol.Message {
	t.Helper()
	for _, message := range messages {
		if string(message.ID) == id {
			return message
		}
	}
	t.Fatalf("response id %s not found in %#v", id, messages)
	return protocol.Message{}
}

func decodeDiagnostics(t *testing.T, message protocol.Message) protocol.PublishDiagnosticsParams {
	t.Helper()
	if message.Method != "textDocument/publishDiagnostics" {
		t.Fatalf("method = %q, want publishDiagnostics", message.Method)
	}
	var params protocol.PublishDiagnosticsParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	return params
}

func frameJSON(message any) string {
	payload, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	return frame(string(payload))
}

func frame(payload string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
}
