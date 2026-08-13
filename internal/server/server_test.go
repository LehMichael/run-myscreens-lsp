package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"testing"

	"example.com/run-myscreens-lsp/internal/protocol"
	"example.com/run-myscreens-lsp/internal/syntax"
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
