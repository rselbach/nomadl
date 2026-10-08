package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
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
	// TimeInferred reports that the line carried no timestamp of its own
	// and Timestamp is an estimate.
	TimeInferred bool
}

// SearchFilters selects log rows. Results are newest first; After, when
// set, continues a previous page from its last row.
type SearchFilters struct {
	Query  string
	Jobs   []string
	Stream string
	Since  time.Time
	Until  time.Time
	Limit  int
	After  *Cursor
}

// Cursor is a position in the newest-first result order: the timestamp
// and id of the last row of a page.
type Cursor struct {
	Timestamp time.Time
	ID        int64
}

// CursorAfter returns the cursor that continues after entry.
func CursorAfter(entry LogEntry) Cursor {
	return Cursor{Timestamp: entry.Timestamp, ID: entry.ID}
}

// String encodes the cursor for use in a URL.
func (c Cursor) String() string {
	return strconv.FormatInt(c.Timestamp.UnixNano(), 10) + "-" + strconv.FormatInt(c.ID, 10)
}

// ParseCursor decodes a cursor produced by Cursor.String.
func ParseCursor(value string) (Cursor, error) {
	nanos, id, ok := strings.Cut(value, "-")
	if !ok {
		return Cursor{}, fmt.Errorf("invalid cursor %q", value)
	}
	ns, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return Cursor{}, fmt.Errorf("invalid cursor %q: %w", value, err)
	}
	rowID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return Cursor{}, fmt.Errorf("invalid cursor %q: %w", value, err)
	}
	return Cursor{Timestamp: time.Unix(0, ns).UTC(), ID: rowID}, nil
}

// Store keeps log rows in SQLite. Writes go through one connection;
// queries use a separate read-only pool, so in WAL mode they neither
// wait for ingestion nor hold it up.
type Store struct {
	db *sql.DB
	ro *sql.DB
}

const (
	writerPragmas = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	readerPragmas = "?_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	readerConns   = 4
)

// timestampLayout is fixed-width and UTC-normalized so that the TEXT
// timestamp column sorts correctly under lexicographic comparison;
// RFC3339Nano trims trailing zeros and preserves offsets, both of which
// break string ordering.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTimestamp(t time.Time) string {
	return t.UTC().Format(timestampLayout)
}

// New opens the database at dbPath, creating or migrating the schema.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+writerPragmas)
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

	ro, err := sql.Open("sqlite", dbPath+readerPragmas)
	if err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("open sqlite reader: %w; close sqlite: %v", err, closeErr)
		}
		return nil, fmt.Errorf("open sqlite reader: %w", err)
	}
	ro.SetMaxOpenConns(readerConns)

	return &Store{db: db, ro: ro}, nil
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
		time_inferred INTEGER NOT NULL DEFAULT 0,
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
	if err := ensureColumn(db, "logs", "time_inferred", "INTEGER NOT NULL DEFAULT 0"); err != nil {
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

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO logs (timestamp, job, alloc_id, task, level, message, raw, stream, line_ref, time_inferred) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
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
			e.TimeInferred,
		)
		if err != nil {
			return fmt.Errorf("insert: %w", err)
		}
	}

	return tx.Commit()
}

// Search returns up to f.Limit rows matching f, newest first.
func (s *Store) Search(ctx context.Context, f SearchFilters) ([]LogEntry, error) {
	if f.Limit == 0 {
		f.Limit = 500
	}

	where, args, err := searchWhere(f)
	if err != nil {
		return nil, err
	}
	if f.After != nil {
		ts := formatTimestamp(f.After.Timestamp)
		where += " AND (timestamp < ? OR (timestamp = ? AND id < ?))"
		args = append(args, ts, ts, f.After.ID)
	}
	args = append(args, f.Limit)
	rows, err := s.ro.QueryContext(ctx, `
		SELECT id, timestamp, job, alloc_id, task, level, message, raw, stream, line_ref, time_inferred
		FROM logs
		WHERE `+where+`
		ORDER BY timestamp DESC, id DESC
		LIMIT ?
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
			return "", nil, err
		}
		queryClause, queryArgs, err := node.sql()
		if err != nil {
			return "", nil, err
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
		if err := rows.Scan(&e.ID, &tsStr, &e.Job, &e.AllocID, &e.Task, &e.Level, &e.Message, &e.Raw, &e.Stream, &e.LineRef, &e.TimeInferred); err != nil {
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
func (s *Store) CountFiltered(ctx context.Context, f SearchFilters) (int, error) {
	where, args, err := searchWhere(f)
	if err != nil {
		return 0, err
	}

	var count int
	if err := s.ro.QueryRowContext(ctx, "SELECT COUNT(*) FROM logs WHERE "+where, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count filtered: %w", err)
	}
	return count, nil
}

// HistogramBin counts the rows in one histogram interval, in total and
// per level bucket.
type HistogramBin struct {
	Count  int
	Levels map[string]int
}

// Histogram is the distribution of matching rows over time.
type Histogram struct {
	Start    time.Time
	End      time.Time
	Interval time.Duration
	Total    int
	Bins     []HistogramBin
}

// Histogram buckets all rows matching f (ignoring limit and cursor) into
// binCount equal intervals, counting each level bucket separately. The
// window is f.Since/f.Until when set, otherwise the earliest and latest
// matching timestamps.
func (s *Store) Histogram(ctx context.Context, f SearchFilters, binCount int) (Histogram, error) {
	if binCount <= 0 {
		binCount = 60
	}

	where, args, err := searchWhere(f)
	if err != nil {
		return Histogram{}, err
	}

	start, end := f.Since.UTC(), f.Until.UTC()
	if f.Since.IsZero() || f.Until.IsZero() {
		var minStr, maxStr sql.NullString
		if err := s.ro.QueryRowContext(ctx, "SELECT MIN(timestamp), MAX(timestamp) FROM logs WHERE "+where, args...).Scan(&minStr, &maxStr); err != nil {
			return Histogram{}, fmt.Errorf("histogram bounds: %w", err)
		}
		if !minStr.Valid || !maxStr.Valid {
			return Histogram{}, nil
		}
		if f.Since.IsZero() {
			if start, err = time.Parse(time.RFC3339Nano, minStr.String); err != nil {
				return Histogram{}, fmt.Errorf("parse histogram start %q: %w", minStr.String, err)
			}
		}
		if f.Until.IsZero() {
			if end, err = time.Parse(time.RFC3339Nano, maxStr.String); err != nil {
				return Histogram{}, fmt.Errorf("parse histogram end %q: %w", maxStr.String, err)
			}
		}
	}
	span := end.Sub(start)
	if span <= 0 {
		span = time.Second
		end = start.Add(span)
	}

	// julianday gives fractional days, preserving sub-second resolution
	// that unixepoch would truncate.
	binsPerDay := float64(binCount) / (span.Seconds() / 86400.0)
	query := `
		SELECT CAST((julianday(timestamp) - julianday(?)) * ? AS INTEGER) AS bin,
		       UPPER(level),
		       COUNT(*)
		FROM logs
		WHERE ` + where + `
		GROUP BY bin, UPPER(level)`
	queryArgs := append([]any{formatTimestamp(start), binsPerDay}, args...)

	rows, err := s.ro.QueryContext(ctx, query, queryArgs...)
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
		var bin, count int
		var level string
		if err := rows.Scan(&bin, &level, &count); err != nil {
			return Histogram{}, fmt.Errorf("scan histogram bin: %w", err)
		}
		bin = min(max(bin, 0), binCount-1)
		if h.Bins[bin].Levels == nil {
			h.Bins[bin].Levels = make(map[string]int)
		}
		h.Bins[bin].Count += count
		h.Bins[bin].Levels[LevelBucket(level)] += count
		h.Total += count
	}
	if err := rows.Err(); err != nil {
		return Histogram{}, fmt.Errorf("iterate histogram bins: %w", err)
	}
	return h, nil
}

// SearchAfter returns entries with an id greater than afterID that
// match f, oldest first. It backs incremental tailing.
func (s *Store) SearchAfter(ctx context.Context, afterID int64, f SearchFilters) ([]LogEntry, error) {
	if f.Limit == 0 {
		f.Limit = 500
	}

	where, args, err := searchWhere(f)
	if err != nil {
		return nil, err
	}
	args = append([]any{afterID}, args...)
	args = append(args, f.Limit)
	rows, err := s.ro.QueryContext(ctx, `
		SELECT id, timestamp, job, alloc_id, task, level, message, raw, stream, line_ref, time_inferred
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

// Around returns up to n rows on each side of the row with the given id
// from the same task stream, oldest first, including that row. Ids follow
// insertion order, which is the order of the lines in the task's log. It
// returns no rows when id doesn't exist.
func (s *Store) Around(ctx context.Context, id int64, n int) ([]LogEntry, error) {
	var allocID, task, stream string
	err := s.ro.QueryRowContext(ctx, "SELECT alloc_id, task, stream FROM logs WHERE id = ?", id).Scan(&allocID, &task, &stream)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up row %d: %w", id, err)
	}

	const columns = "id, timestamp, job, alloc_id, task, level, message, raw, stream, line_ref, time_inferred"
	rows, err := s.ro.QueryContext(ctx, `
		SELECT `+columns+` FROM logs
		WHERE alloc_id = ? AND task = ? AND stream = ? AND id < ?
		ORDER BY id DESC LIMIT ?
	`, allocID, task, stream, id, n)
	if err != nil {
		return nil, fmt.Errorf("rows before %d: %w", id, err)
	}
	before, err := scanEntries(rows)
	if err != nil {
		return nil, err
	}
	slices.Reverse(before)

	rows, err = s.ro.QueryContext(ctx, `
		SELECT `+columns+` FROM logs
		WHERE alloc_id = ? AND task = ? AND stream = ? AND id >= ?
		ORDER BY id ASC LIMIT ?
	`, allocID, task, stream, id, n+1)
	if err != nil {
		return nil, fmt.Errorf("rows after %d: %w", id, err)
	}
	after, err := scanEntries(rows)
	if err != nil {
		return nil, err
	}
	return append(before, after...), nil
}

// MaxID returns the highest log row id, or 0 for an empty store.
func (s *Store) MaxID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	if err := s.ro.QueryRowContext(ctx, "SELECT MAX(id) FROM logs").Scan(&id); err != nil {
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

// Clear deletes every stored log row.
func (s *Store) Clear(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM logs")
	if err != nil {
		return fmt.Errorf("clear: %w", err)
	}
	return nil
}

// Count returns the number of stored log rows.
func (s *Store) Count(ctx context.Context) (int, error) {
	var count int
	err := s.ro.QueryRowContext(ctx, "SELECT COUNT(*) FROM logs").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return count, nil
}

// Close closes the reader pool and the writer connection.
func (s *Store) Close() error {
	return errors.Join(s.ro.Close(), s.db.Close())
}
