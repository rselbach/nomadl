package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type LogEntry struct {
	ID        int64
	Timestamp time.Time
	Job       string
	AllocID   string
	Task      string
	Level     string
	Message   string
	Raw       string
	Stream    string
	// LineRef identifies the line's position in its source log file
	// ("<file>@<offset>"). It is stable across refetches of the same
	// file, unlike parsed timestamps, so it drives deduplication.
	LineRef string
}

type SearchFilters struct {
	Query  string
	Jobs   []string
	Stream string
	Since  time.Time
	Until  time.Time
	Limit  int
	Offset int
}

type Store struct {
	db *sql.DB
}

// timestampLayout is fixed-width and UTC-normalized so that the TEXT
// timestamp column sorts correctly under lexicographic comparison;
// RFC3339Nano trims trailing zeros and preserves offsets, both of which
// break string ordering.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTimestamp(t time.Time) string {
	return t.UTC().Format(timestampLayout)
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := initSchema(db); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("init schema: %w; close sqlite: %v", err, closeErr)
		}
		return nil, fmt.Errorf("init schema: %w", err)
	}

	return &Store{db: db}, nil
}

func initSchema(db *sql.DB) error {
	tableSchema := `
	CREATE TABLE IF NOT EXISTS logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		job TEXT NOT NULL,
		alloc_id TEXT NOT NULL,
		task TEXT NOT NULL,
		level TEXT NOT NULL DEFAULT 'UNKNOWN',
		message TEXT NOT NULL,
		raw TEXT NOT NULL DEFAULT '',
		stream TEXT NOT NULL DEFAULT 'stdout',
		line_ref TEXT NOT NULL DEFAULT '',
		fetched_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	`

	_, err := db.Exec(tableSchema)
	if err != nil {
		return fmt.Errorf("exec table schema: %w", err)
	}
	if err := ensureColumn(db, "logs", "raw", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "logs", "line_ref", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	schema := `
	CREATE INDEX IF NOT EXISTS idx_logs_timestamp ON logs(timestamp);
	CREATE INDEX IF NOT EXISTS idx_logs_job ON logs(job);
	CREATE INDEX IF NOT EXISTS idx_logs_task ON logs(task);
	CREATE INDEX IF NOT EXISTS idx_logs_level ON logs(level);
	CREATE INDEX IF NOT EXISTS idx_logs_alloc_id ON logs(alloc_id);
	DROP INDEX IF EXISTS idx_logs_dedupe;
	CREATE UNIQUE INDEX IF NOT EXISTS idx_logs_line_ref ON logs(alloc_id, line_ref) WHERE line_ref <> '';

	DROP TRIGGER IF EXISTS logs_ai;
	DROP TRIGGER IF EXISTS logs_ad;
	DROP TRIGGER IF EXISTS logs_au;
	DROP TABLE IF EXISTS logs_fts;
	`

	_, err = db.Exec(schema)
	if err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}

	return nil
}

func ensureColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			fmt.Printf("warning: close table info rows: %v\n", err)
		}
	}()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("scan table info: %w", err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table info: %w", err)
	}

	if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

func (s *Store) InsertLogs(entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			fmt.Printf("warning: rollback insert logs: %v\n", err)
		}
	}()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO logs (timestamp, job, alloc_id, task, level, message, raw, stream, line_ref) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer func() {
		if err := stmt.Close(); err != nil {
			fmt.Printf("warning: close insert statement: %v\n", err)
		}
	}()

	for _, e := range entries {
		_, err := stmt.Exec(
			formatTimestamp(e.Timestamp),
			e.Job,
			e.AllocID,
			e.Task,
			e.Level,
			e.Message,
			e.Raw,
			e.Stream,
			e.LineRef,
		)
		if err != nil {
			return fmt.Errorf("insert: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) InsertLog(entry LogEntry) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO logs (timestamp, job, alloc_id, task, level, message, raw, stream, line_ref) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		formatTimestamp(entry.Timestamp),
		entry.Job,
		entry.AllocID,
		entry.Task,
		entry.Level,
		entry.Message,
		entry.Raw,
		entry.Stream,
		entry.LineRef,
	)
	if err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	return nil
}

func (s *Store) Search(f SearchFilters) ([]LogEntry, error) {
	if f.Limit == 0 {
		f.Limit = 500
	}

	where, args, err := searchWhere(f)
	if err != nil {
		return nil, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.Query(`
		SELECT id, timestamp, job, alloc_id, task, level, message, raw, stream, line_ref
		FROM logs
		WHERE `+where+`
		ORDER BY timestamp DESC, id DESC
		LIMIT ? OFFSET ?
	`, args...)
	if err != nil {
		return nil, err
	}

	return scanEntries(rows)
}

func searchWhere(f SearchFilters) (string, []any, error) {
	clauses := []string{"1=1"}
	args := []any{}
	if jobClause, jobArgs := inClause("job", f.Jobs); jobClause != "" {
		clauses = append(clauses, strings.TrimPrefix(jobClause, " AND "))
		args = append(args, jobArgs...)
	}
	if f.Stream != "" {
		clauses = append(clauses, "stream = ?")
		args = append(args, f.Stream)
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, formatTimestamp(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "timestamp <= ?")
		args = append(args, formatTimestamp(f.Until))
	}
	if strings.TrimSpace(f.Query) != "" {
		node, err := parseLogQuery(f.Query)
		if err != nil {
			return "", nil, fmt.Errorf("parse query: %w", err)
		}
		queryClause, queryArgs, err := node.sql()
		if err != nil {
			return "", nil, fmt.Errorf("compile query: %w", err)
		}
		clauses = append(clauses, "("+queryClause+")")
		args = append(args, queryArgs...)
	}
	return strings.Join(clauses, " AND "), args, nil
}

func inClause(column string, values []string) (string, []any) {
	if len(values) == 0 {
		return "", nil
	}

	placeholders := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		placeholders = append(placeholders, "?")
		args = append(args, value)
	}
	if len(args) == 0 {
		return "", nil
	}
	return " AND " + column + " IN (" + strings.Join(placeholders, ",") + ")", args
}

func scanEntries(rows *sql.Rows) (entries []LogEntry, err error) {
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close rows: %w", closeErr)
		}
	}()

	for rows.Next() {
		var e LogEntry
		var tsStr string
		if err := rows.Scan(&e.ID, &tsStr, &e.Job, &e.AllocID, &e.Task, &e.Level, &e.Message, &e.Raw, &e.Stream, &e.LineRef); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if e.Raw == "" {
			e.Raw = e.Message
		}
		t, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			fallbackTime, fallbackErr := time.Parse(time.RFC3339, tsStr)
			if fallbackErr != nil {
				return nil, fmt.Errorf("parse timestamp %q: %w", tsStr, err)
			}
			t = fallbackTime
		}
		e.Timestamp = t
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// CountFiltered returns the number of rows matching f, ignoring limit
// and offset.
func (s *Store) CountFiltered(f SearchFilters) (int, error) {
	where, args, err := searchWhere(f)
	if err != nil {
		return 0, err
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM logs WHERE "+where, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count filtered: %w", err)
	}
	return count, nil
}

type HistogramBin struct {
	Count  int
	Errors int
}

type Histogram struct {
	Start    time.Time
	End      time.Time
	Interval time.Duration
	Total    int
	Errors   int
	Bins     []HistogramBin
}

// Histogram buckets all rows matching f (ignoring limit and offset)
// into binCount equal intervals. The window is f.Since/f.Until when
// set, otherwise the earliest and latest matching timestamps. Rows in
// the error and emergency level buckets are counted separately.
func (s *Store) Histogram(f SearchFilters, binCount int) (Histogram, error) {
	if binCount <= 0 {
		binCount = 60
	}

	where, args, err := searchWhere(f)
	if err != nil {
		return Histogram{}, err
	}

	var minStr, maxStr sql.NullString
	if err := s.db.QueryRow("SELECT MIN(timestamp), MAX(timestamp) FROM logs WHERE "+where, args...).Scan(&minStr, &maxStr); err != nil {
		return Histogram{}, fmt.Errorf("histogram bounds: %w", err)
	}
	if !minStr.Valid || !maxStr.Valid {
		return Histogram{}, nil
	}
	start, err := time.Parse(time.RFC3339Nano, minStr.String)
	if err != nil {
		return Histogram{}, fmt.Errorf("parse histogram start %q: %w", minStr.String, err)
	}
	end, err := time.Parse(time.RFC3339Nano, maxStr.String)
	if err != nil {
		return Histogram{}, fmt.Errorf("parse histogram end %q: %w", maxStr.String, err)
	}
	if !f.Since.IsZero() {
		start = f.Since.UTC()
	}
	if !f.Until.IsZero() {
		end = f.Until.UTC()
	}
	span := end.Sub(start)
	if span <= 0 {
		span = time.Second
		end = start.Add(span)
	}

	errLevels := errorLevels()
	placeholders := make([]string, 0, len(errLevels))
	errArgs := make([]any, 0, len(errLevels))
	for _, level := range errLevels {
		placeholders = append(placeholders, "?")
		errArgs = append(errArgs, level)
	}

	// julianday gives fractional days, preserving sub-second resolution
	// that unixepoch would truncate.
	binsPerDay := float64(binCount) / (span.Seconds() / 86400.0)
	query := `
		SELECT CAST((julianday(timestamp) - julianday(?)) * ? AS INTEGER) AS bin,
		       COUNT(*),
		       SUM(CASE WHEN UPPER(level) IN (` + strings.Join(placeholders, ",") + `) THEN 1 ELSE 0 END)
		FROM logs
		WHERE ` + where + `
		GROUP BY bin`
	queryArgs := append([]any{formatTimestamp(start), binsPerDay}, errArgs...)
	queryArgs = append(queryArgs, args...)

	rows, err := s.db.Query(query, queryArgs...)
	if err != nil {
		return Histogram{}, fmt.Errorf("histogram bins: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			fmt.Printf("warning: close histogram rows: %v\n", err)
		}
	}()

	h := Histogram{
		Start:    start,
		End:      end,
		Interval: span / time.Duration(binCount),
		Bins:     make([]HistogramBin, binCount),
	}
	for rows.Next() {
		var bin, count, errCount int
		if err := rows.Scan(&bin, &count, &errCount); err != nil {
			return Histogram{}, fmt.Errorf("scan histogram bin: %w", err)
		}
		if bin < 0 {
			bin = 0
		}
		if bin >= binCount {
			bin = binCount - 1
		}
		h.Bins[bin].Count += count
		h.Bins[bin].Errors += errCount
		h.Total += count
		h.Errors += errCount
	}
	if err := rows.Err(); err != nil {
		return Histogram{}, fmt.Errorf("iterate histogram bins: %w", err)
	}
	return h, nil
}

// SearchAfter returns entries with an id greater than afterID that
// match f, oldest first. It backs incremental tailing.
func (s *Store) SearchAfter(afterID int64, f SearchFilters) ([]LogEntry, error) {
	if f.Limit == 0 {
		f.Limit = 500
	}

	where, args, err := searchWhere(f)
	if err != nil {
		return nil, err
	}
	args = append([]any{afterID}, args...)
	args = append(args, f.Limit)
	rows, err := s.db.Query(`
		SELECT id, timestamp, job, alloc_id, task, level, message, raw, stream, line_ref
		FROM logs
		WHERE id > ? AND `+where+`
		ORDER BY id ASC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}

	return scanEntries(rows)
}

// MaxID returns the highest log row id, or 0 for an empty store.
func (s *Store) MaxID() (int64, error) {
	var id sql.NullInt64
	if err := s.db.QueryRow("SELECT MAX(id) FROM logs").Scan(&id); err != nil {
		return 0, fmt.Errorf("max id: %w", err)
	}
	return id.Int64, nil
}

// Prune deletes the oldest rows beyond maxRows, keeping long sessions
// from growing the database without bound. It reports how many rows
// were deleted.
func (s *Store) Prune(maxRows int) (int64, error) {
	if maxRows <= 0 {
		return 0, nil
	}

	// The subquery finds the id of the maxRows-th newest row; with
	// fewer rows it yields NULL and nothing matches.
	result, err := s.db.Exec(`
		DELETE FROM logs
		WHERE id < (SELECT id FROM logs ORDER BY id DESC LIMIT 1 OFFSET ?)
	`, maxRows-1)
	if err != nil {
		return 0, fmt.Errorf("prune: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune rows affected: %w", err)
	}
	return deleted, nil
}

func (s *Store) Clear() error {
	_, err := s.db.Exec("DELETE FROM logs")
	if err != nil {
		return fmt.Errorf("clear: %w", err)
	}
	return nil
}

func (s *Store) Count() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return count, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
