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
}

type SearchFilters struct {
	Query  string
	Job    string
	Jobs   []string
	Task   string
	Level  string
	Levels []string
	Stream string
	Since  time.Time
	Until  time.Time
	Limit  int
	Offset int
}

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
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

	schema := `
	CREATE INDEX IF NOT EXISTS idx_logs_timestamp ON logs(timestamp);
	CREATE INDEX IF NOT EXISTS idx_logs_job ON logs(job);
	CREATE INDEX IF NOT EXISTS idx_logs_task ON logs(task);
	CREATE INDEX IF NOT EXISTS idx_logs_level ON logs(level);
	CREATE INDEX IF NOT EXISTS idx_logs_alloc_id ON logs(alloc_id);
	DELETE FROM logs
	WHERE id NOT IN (
		SELECT MIN(id)
		FROM logs
		GROUP BY timestamp, job, alloc_id, task, level, stream, message, raw
	);
	DROP INDEX IF EXISTS idx_logs_dedupe;
	CREATE UNIQUE INDEX IF NOT EXISTS idx_logs_dedupe ON logs(timestamp, job, alloc_id, task, level, stream, message, raw);

	CREATE VIRTUAL TABLE IF NOT EXISTS logs_fts USING fts5(
		message,
		content='logs',
		content_rowid='id'
	);

	CREATE TRIGGER IF NOT EXISTS logs_ai AFTER INSERT ON logs BEGIN
		INSERT INTO logs_fts(rowid, message) VALUES (new.id, new.message);
	END;

	CREATE TRIGGER IF NOT EXISTS logs_ad AFTER DELETE ON logs BEGIN
		INSERT INTO logs_fts(logs_fts, rowid, message) VALUES('delete', old.id, old.message);
	END;

	CREATE TRIGGER IF NOT EXISTS logs_au AFTER UPDATE ON logs BEGIN
		INSERT INTO logs_fts(logs_fts, rowid, message) VALUES('delete', old.id, old.message);
		INSERT INTO logs_fts(rowid, message) VALUES (new.id, new.message);
	END;
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

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO logs (timestamp, job, alloc_id, task, level, message, raw, stream) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
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
			e.Timestamp.Format(time.RFC3339Nano),
			e.Job,
			e.AllocID,
			e.Task,
			e.Level,
			e.Message,
			e.Raw,
			e.Stream,
		)
		if err != nil {
			return fmt.Errorf("insert: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) InsertLog(entry LogEntry) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO logs (timestamp, job, alloc_id, task, level, message, raw, stream) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Timestamp.Format(time.RFC3339Nano),
		entry.Job,
		entry.AllocID,
		entry.Task,
		entry.Level,
		entry.Message,
		entry.Raw,
		entry.Stream,
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
		SELECT id, timestamp, job, alloc_id, task, level, message, raw, stream
		FROM logs
		WHERE `+where+`
		ORDER BY timestamp DESC
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
	if f.Job != "" {
		clauses = append(clauses, "job = ?")
		args = append(args, f.Job)
	}
	if jobClause, jobArgs := inClause("job", f.Jobs); jobClause != "" {
		clauses = append(clauses, strings.TrimPrefix(jobClause, " AND "))
		args = append(args, jobArgs...)
	}
	if f.Task != "" {
		clauses = append(clauses, "task = ?")
		args = append(args, f.Task)
	}
	if f.Level != "" {
		clauses = append(clauses, "level = ?")
		args = append(args, f.Level)
	}
	if levelClause, levelArgs := inClause("level", f.Levels); levelClause != "" {
		clauses = append(clauses, strings.TrimPrefix(levelClause, " AND "))
		args = append(args, levelArgs...)
	}
	if f.Stream != "" {
		clauses = append(clauses, "stream = ?")
		args = append(args, f.Stream)
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "timestamp <= ?")
		args = append(args, f.Until.UTC().Format(time.RFC3339Nano))
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
		if err := rows.Scan(&e.ID, &tsStr, &e.Job, &e.AllocID, &e.Task, &e.Level, &e.Message, &e.Raw, &e.Stream); err != nil {
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
