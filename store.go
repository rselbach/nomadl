package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const searchHistoryLimit = 100

const (
	preferenceLogType       = "log_type"
	preferenceWrapLogs      = "wrap_logs"
	preferenceFollow        = "follow"
	preferenceHighlightJSON = "highlight_json"
)

type appStore struct {
	db *sql.DB
}

func defaultStorePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return storePath(configDir), nil
}

func storePath(configDir string) string {
	return filepath.Join(configDir, "nomadl", "nomadl.db")
}

func openAppStore(path string) (*appStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("store path cannot be empty")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &appStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (store *appStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *appStore) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS search_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			query TEXT NOT NULL,
			service TEXT NOT NULL DEFAULT '',
			namespace TEXT NOT NULL DEFAULT '',
			region TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS search_history_recent
			ON search_history(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS search_history_context_query
			ON search_history(namespace, region, service, query)`,
		`CREATE TABLE IF NOT EXISTS preferences (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	}

	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate store: %w", err)
		}
	}
	return nil
}

func (store *appStore) LoadPreferences(defaults appPreferences) (appPreferences, error) {
	if store == nil {
		return defaults, nil
	}

	rows, err := store.db.QueryContext(context.Background(), `SELECT key, value FROM preferences`)
	if err != nil {
		return defaults, fmt.Errorf("load preferences: %w", err)
	}
	defer rows.Close()

	preferences := defaults
	for rows.Next() {
		var key string
		var value string
		if err := rows.Scan(&key, &value); err != nil {
			return defaults, fmt.Errorf("scan preferences: %w", err)
		}
		switch key {
		case preferenceLogType:
			if isValidLogType(value) {
				preferences.logType = value
			}
		case preferenceWrapLogs:
			if parsed, ok := parseStoredBool(value); ok {
				preferences.wrapLogs = parsed
			}
		case preferenceFollow:
			if parsed, ok := parseStoredBool(value); ok {
				preferences.follow = parsed
			}
		case preferenceHighlightJSON:
			if parsed, ok := parseStoredBool(value); ok {
				preferences.highlightJSON = parsed
			}
		}
	}
	if err := rows.Err(); err != nil {
		return defaults, fmt.Errorf("read preferences: %w", err)
	}
	return preferences, nil
}

func (store *appStore) SavePreferences(preferences appPreferences) error {
	if store == nil {
		return nil
	}

	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin preferences transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UnixNano()
	values := map[string]string{
		preferenceLogType:       preferences.logType,
		preferenceWrapLogs:      strconv.FormatBool(preferences.wrapLogs),
		preferenceFollow:        strconv.FormatBool(preferences.follow),
		preferenceHighlightJSON: strconv.FormatBool(preferences.highlightJSON),
	}
	for key, value := range values {
		_, err := tx.ExecContext(ctx, `INSERT INTO preferences (key, value, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				value = excluded.value,
				updated_at = excluded.updated_at`,
			key, value, now)
		if err != nil {
			return fmt.Errorf("save preference %q: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit preferences: %w", err)
	}
	return nil
}

func parseStoredBool(value string) (bool, bool) {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, false
	}
	return parsed, true
}

func (store *appStore) SaveSearch(query string, historyContext searchHistoryContext) error {
	if store == nil {
		return nil
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}

	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin search history transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `DELETE FROM search_history
		WHERE query = ? AND service = ? AND namespace = ? AND region = ?`,
		query, historyContext.service, historyContext.namespace, historyContext.region)
	if err != nil {
		return fmt.Errorf("dedupe search history: %w", err)
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO search_history
		(query, service, namespace, region, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		query, historyContext.service, historyContext.namespace, historyContext.region, time.Now().UnixNano())
	if err != nil {
		return fmt.Errorf("save search history: %w", err)
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM search_history
		WHERE id NOT IN (
			SELECT id FROM search_history
			ORDER BY created_at DESC
			LIMIT ?
		)`, searchHistoryLimit)
	if err != nil {
		return fmt.Errorf("trim search history: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit search history: %w", err)
	}
	return nil
}

func (store *appStore) RecentSearches(historyContext searchHistoryContext, limit int) ([]string, error) {
	if store == nil {
		return nil, nil
	}
	if limit < 1 {
		limit = searchHistoryLimit
	}

	rows, err := store.db.QueryContext(context.Background(), `SELECT query
		FROM (
			SELECT
				query,
				MAX(created_at) AS recent,
				MAX(CASE
					WHEN service = ? AND namespace = ? AND region = ? THEN 1
					ELSE 0
				END) AS context_match
			FROM search_history
			GROUP BY query
		)
		ORDER BY context_match DESC, recent DESC
		LIMIT ?`, historyContext.service, historyContext.namespace, historyContext.region, limit)
	if err != nil {
		return nil, fmt.Errorf("load search history: %w", err)
	}
	defer rows.Close()

	var queries []string
	for rows.Next() {
		var query string
		if err := rows.Scan(&query); err != nil {
			return nil, fmt.Errorf("scan search history: %w", err)
		}
		queries = append(queries, query)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read search history: %w", err)
	}
	return queries, nil
}

type searchHistoryContext struct {
	service   string
	namespace string
	region    string
}
