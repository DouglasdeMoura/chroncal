package event

import "github.com/douglasdemoura/tcal/internal/storage"

type Service struct {
	q *storage.Queries
}

func NewService(q *storage.Queries) *Service {
	return &Service{q: q}
}
