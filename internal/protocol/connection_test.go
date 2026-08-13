package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

func TestConnectionReadAndReply(t *testing.T) {
	input := frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	var output bytes.Buffer
	connection := NewConnection(bytes.NewBufferString(input), &output)

	message, err := connection.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if message.Method != "initialize" || string(message.ID) != "1" {
		t.Fatalf("unexpected message: %#v", message)
	}
	if err := connection.Reply(message.ID, map[string]bool{"ok": true}); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	response, err := NewConnection(&output, &bytes.Buffer{}).Read()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var result map[string]bool
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !result["ok"] {
		t.Fatalf("result = %#v, want ok", result)
	}
}

func TestConnectionRejectsMissingContentLength(t *testing.T) {
	connection := NewConnection(bytes.NewBufferString("X-Test: true\r\n\r\n{}"), &bytes.Buffer{})
	if _, err := connection.Read(); err == nil {
		t.Fatal("Read succeeded without Content-Length")
	}
}

func frame(payload string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
}
