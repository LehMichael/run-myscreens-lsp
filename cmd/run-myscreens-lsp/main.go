package main

import (
	"context"
	"log"
	"os"

	"example.com/run-myscreens-lsp/internal/protocol"
	"example.com/run-myscreens-lsp/internal/server"
	"example.com/run-myscreens-lsp/internal/syntax"
)

func main() {
	logger := log.New(os.Stderr, "run-myscreens-lsp: ", log.LstdFlags)
	connection := protocol.NewConnection(os.Stdin, os.Stdout)
	languageServer := server.New(connection, syntax.NewTreeSitterAnalyzer(), logger)
	if err := languageServer.Run(context.Background()); err != nil {
		logger.Print(err)
		os.Exit(1)
	}
}
