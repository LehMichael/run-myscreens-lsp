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

func TestProtocolLifecyclePublishesAndClearsDiagnostics(t *testing.T) {
	uri := "file:///workspace/test.com"
	input := bytes.NewBufferString(
		frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`) +
			frame(`{"jsonrpc":"2.0","method":"initialized","params":{}}`) +
			frame(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"`+uri+`","languageId":"run-myscreens","version":1,"text":"//M(Test)\nLOAD\nEND_PRESS\n//END\n"}}}`) +
			frame(`{"jsonrpc":"2.0","method":"textDocument/didChange","params":{"textDocument":{"uri":"`+uri+`","version":2},"contentChanges":[{"text":"//M(Test)\nLOAD\nEND_LOAD\n//END\n"}]}}`) +
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

	reader := protocol.NewConnection(&output, io.Discard)
	var messages []protocol.Message
	for {
		message, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		messages = append(messages, message)
	}
	if len(messages) != 5 {
		t.Fatalf("message count = %d, want 5: %#v", len(messages), messages)
	}

	var initialize protocol.InitializeResult
	if err := json.Unmarshal(messages[0].Result, &initialize); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if initialize.Capabilities.PositionEncoding != "utf-16" || initialize.Capabilities.TextDocumentSync.Change != 1 {
		t.Fatalf("unexpected capabilities: %#v", initialize.Capabilities)
	}

	openDiagnostics := decodeDiagnostics(t, messages[1])
	if len(openDiagnostics.Diagnostics) != 1 || openDiagnostics.Diagnostics[0].Code != "mismatched-event-end" {
		t.Fatalf("open diagnostics = %#v", openDiagnostics.Diagnostics)
	}
	changeDiagnostics := decodeDiagnostics(t, messages[2])
	if len(changeDiagnostics.Diagnostics) != 0 || changeDiagnostics.Version == nil || *changeDiagnostics.Version != 2 {
		t.Fatalf("change diagnostics = %#v", changeDiagnostics)
	}
	closeDiagnostics := decodeDiagnostics(t, messages[3])
	if len(closeDiagnostics.Diagnostics) != 0 || closeDiagnostics.Version != nil {
		t.Fatalf("close diagnostics = %#v", closeDiagnostics)
	}
	if string(messages[4].ID) != "2" || string(messages[4].Result) != "null" {
		t.Fatalf("shutdown response = %#v", messages[4])
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

func frame(payload string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
}
