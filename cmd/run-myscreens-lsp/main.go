package main

import (
	"context"
	"log"
	"os"

	"github.com/LehMichael/run-myscreens-lsp/internal/protocol"
	"github.com/LehMichael/run-myscreens-lsp/internal/server"
	"github.com/LehMichael/run-myscreens-lsp/internal/syntax"
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
