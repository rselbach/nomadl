package store

import (
	"context"
	"fmt"
	"log/slog"
)

// FacetCount is how many matching rows have one value of a facet field.
type FacetCount struct {
	Value string
	Count int
}

// FacetCounts counts the rows matching f per service and per level
// bucket. Each field is counted with the query's own clauses on that
// field removed, so values the query hides still show how many rows they
// would add. Levels come back in LevelBuckets order, services by name.
func (s *Store) FacetCounts(ctx context.Context, f SearchFilters) (map[string][]FacetCount, error) {
	// The unary + keeps SQLite from walking idx_logs_job to group rows,
	// which would scan every row instead of using the time range.
	services, err := s.countBy(ctx, f, "service", "+job")
	if err != nil {
		return nil, err
	}

	rawLevels, err := s.countBy(ctx, f, "level", "UPPER(level)")
	if err != nil {
		return nil, err
	}
	perBucket := make(map[string]int, len(LevelBuckets))
	for _, raw := range rawLevels {
		perBucket[LevelBucket(raw.Value)] += raw.Count
	}
	levels := make([]FacetCount, 0, len(LevelBuckets))
	for _, bucket := range LevelBuckets {
		levels = append(levels, FacetCount{Value: bucket, Count: perBucket[bucket]})
	}

	return map[string][]FacetCount{"service": services, "level": levels}, nil
}

// countBy groups the rows matching f, minus f's clauses on field, by the
// SQL expression expr.
func (s *Store) countBy(ctx context.Context, f SearchFilters, field, expr string) ([]FacetCount, error) {
	query, err := SetSelection(f.Query, field, Selection{Mode: SelectAll})
	if err != nil {
		return nil, err
	}
	f.Query = query
	where, args, err := searchWhere(f)
	if err != nil {
		return nil, err
	}

	rows, err := s.ro.QueryContext(ctx, "SELECT "+expr+", COUNT(*) FROM logs WHERE "+where+" GROUP BY 1 ORDER BY 1", args...)
	if err != nil {
		return nil, fmt.Errorf("count %s values: %w", field, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Warn("close facet counts", "field", field, "err", err)
		}
	}()

	var counts []FacetCount
	for rows.Next() {
		var c FacetCount
		if err := rows.Scan(&c.Value, &c.Count); err != nil {
			return nil, fmt.Errorf("scan %s count: %w", field, err)
		}
		counts = append(counts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s counts: %w", field, err)
	}
	return counts, nil
}
