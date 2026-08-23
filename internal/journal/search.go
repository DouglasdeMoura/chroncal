package journal

import (
	"context"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

type SearchParams struct {
	Query      string
	CalendarID int64  // 0 = all
	Status     string // empty = all
}

type ExportParams struct {
	CalendarID int64  // 0 = all
	From       string // date-only ("YYYY-MM-DD") or empty
	To         string // date-only ("YYYY-MM-DD") or empty
	Category   string // empty = all
	Status     string // empty = all
}

func (s *Service) Search(ctx context.Context, p SearchParams) ([]Journal, error) {
	ftsQuery := storage.FTSQuery(p.Query)
	if ftsQuery == "" {
		return nil, nil
	}
	rows, err := s.q.SearchJournalsFTS(ctx, ftsQuery, p.CalendarID, p.Status)
	if err != nil {
		return nil, fmt.Errorf("search journals: %w", err)
	}
	journals := fromStorageSlice(rows)
	s.populateCategories(ctx, journals)
	return journals, nil
}

func (s *Service) ExportFiltered(ctx context.Context, p ExportParams) ([]Journal, error) {
	rows, err := s.q.ListJournalsForExport(ctx, storage.JournalFilterParams{
		CalendarID:   p.CalendarID,
		FromDate:     p.From,
		ToDate:       p.To,
		Category:     p.Category,
		FilterStatus: p.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("export journals: %w", err)
	}
	journals := fromStorageSlice(rows)
	s.populateCategories(ctx, journals)
	return journals, nil
}
