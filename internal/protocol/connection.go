package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const jsonRPCVersion = "2.0"

type Connection struct {
	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex
}

func NewConnection(reader io.Reader, writer io.Writer) *Connection {
	return &Connection{reader: bufio.NewReader(reader), writer: writer}
}

func (c *Connection) Read() (Message, error) {
	contentLength := -1
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && line == "" {
				return Message{}, io.EOF
			}
			return Message{}, fmt.Errorf("read header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return Message{}, fmt.Errorf("malformed header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 {
				return Message{}, fmt.Errorf("invalid Content-Length %q", value)
			}
			contentLength = length
		}
	}
	if contentLength < 0 {
		return Message{}, errors.New("missing Content-Length header")
	}

	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return Message{}, fmt.Errorf("read payload: %w", err)
	}
	var message Message
	if err := json.Unmarshal(payload, &message); err != nil {
		return Message{}, fmt.Errorf("decode JSON-RPC message: %w", err)
	}
	if message.JSONRPC != jsonRPCVersion {
		return Message{}, fmt.Errorf("unsupported JSON-RPC version %q", message.JSONRPC)
	}
	return message, nil
}

func (c *Connection) Reply(id json.RawMessage, result any) error {
	return c.write(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: jsonRPCVersion, ID: id, Result: result})
}

func (c *Connection) ReplyError(id json.RawMessage, responseError ResponseError) error {
	return c.write(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   ResponseError   `json:"error"`
	}{JSONRPC: jsonRPCVersion, ID: id, Error: responseError})
}

func (c *Connection) Notify(method string, params any) error {
	return c.write(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{JSONRPC: jsonRPCVersion, Method: method, Params: params})
}

func (c *Connection) write(message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode JSON-RPC message: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := fmt.Fprintf(c.writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := c.writer.Write(payload); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	return nil
}
