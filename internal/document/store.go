package document

import (
	"sync"

	"github.com/LehMichael/run-myscreens-lsp/internal/model"
)

type Store struct {
	mu        sync.RWMutex
	documents map[string]*Document
}

func NewStore() *Store {
	return &Store{documents: make(map[string]*Document)}
}

func (s *Store) Open(uri, languageID string, version int32, text string) *Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	document := New(uri, languageID, version, text)
	s.documents[uri] = document
	return document
}

func (s *Store) Replace(uri string, version int32, text string) (*Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[uri]
	if !ok {
		return nil, false
	}
	document.Replace(version, text)
	return document, true
}

func (s *Store) SetAnalysis(uri string, version int32, analysis model.Analysis) (*Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[uri]
	if !ok || document.Version != version {
		return nil, false
	}
	document.Analysis = analysis
	return document, true
}

func (s *Store) Get(uri string) (Document, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	document, ok := s.documents[uri]
	if !ok {
		return Document{}, false
	}
	return *document, true
}

func (s *Store) Close(uri string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.documents[uri]; !ok {
		return false
	}
	delete(s.documents, uri)
	return true
}
