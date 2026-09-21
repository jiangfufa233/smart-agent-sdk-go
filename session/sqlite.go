package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model"
)

// SQLiteStore is a keyed multi-session store backed by a SQLite database
// file. One database file holds many independent sessions addressed by id
// (store.Get(id)); each view implements agent.Session. The pure-Go
// modernc.org/sqlite driver is used — no CGO required.
//
// A single-file SQLite database can also be shared by multiple processes;
// writes are serialized via a busy timeout.
type SQLiteStore struct {
	db *sql.DB
}

const sqliteSchema = `CREATE TABLE IF NOT EXISTS session_items (
	session_id TEXT NOT NULL,
	idx        INTEGER NOT NULL,
	item_id    TEXT NOT NULL,
	message    TEXT NOT NULL,
	PRIMARY KEY (session_id, idx),
	UNIQUE (session_id, item_id)
)`

// NewSQLiteStore opens (creating if needed) the database at path and ensures
// the v2 item-ID schema exists. Call Close when done. A database created by
// the v1 message-only schema is rejected and must be migrated explicitly.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	// Reserve the writer before reading indices to avoid cross-connection upgrade races.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("session: open sqlite %s: %w", path, err)
	}
	// One connection: all writes in this process serialize, so SQLITE_BUSY
	// cannot happen locally; cross-process writers wait on busy_timeout.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(sqliteSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("session: init sqlite %s: %w", path, err)
	}
	if err := verifySQLiteSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("session: init sqlite %s: %w", path, err)
	}
	return &SQLiteStore{db: db}, nil
}

func verifySQLiteSchema(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(session_items)")
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	defer func() { _ = rows.Close() }()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("inspect schema: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	for _, column := range []string{"session_id", "idx", "item_id", "message"} {
		if !columns[column] {
			return &FormatUpgradeError{
				Store:  "sqlite",
				Detail: "database uses the legacy v1 session_items schema; migrate data to the v2 item-ID schema before use",
			}
		}
	}
	return nil
}

// Get returns a Session view bound to id.
func (s *SQLiteStore) Get(id string) agent.Session {
	return &sqliteSession{store: s, id: id}
}

// Close releases the database.
func (s *SQLiteStore) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("session: close sqlite: %w", err)
	}
	return nil
}

type sqliteSession struct {
	store *SQLiteStore
	id    string
}

func (s *sqliteSession) List(ctx context.Context, limit int) ([]agent.SessionItem, error) {
	q := "SELECT item_id, message FROM session_items WHERE session_id = ? ORDER BY idx ASC"
	args := []any{s.id}
	if limit > 0 {
		q = "SELECT item_id, message FROM session_items WHERE session_id = ? ORDER BY idx DESC LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.store.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("session: sqlite list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items, err := scanSQLiteItems(rows)
	if err != nil {
		return nil, err
	}
	if limit > 0 {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}
	return cloneSessionItems(items), nil
}

func scanSQLiteItems(rows *sql.Rows) ([]agent.SessionItem, error) {
	var items []agent.SessionItem
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("session: sqlite scan: %w", err)
		}
		var message model.Message
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			return nil, fmt.Errorf("session: sqlite decode: %w", err)
		}
		items = append(items, agent.SessionItem{ID: id, Message: message})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("session: sqlite list: %w", err)
	}
	return items, nil
}

func (s *sqliteSession) Append(ctx context.Context, messages []model.Message) ([]agent.SessionItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items, err := newSessionItems(messages)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("session: sqlite append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var base int
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(idx), -1) FROM session_items WHERE session_id = ?", s.id,
	).Scan(&base); err != nil {
		return nil, fmt.Errorf("session: sqlite append: %w", err)
	}
	for i, item := range items {
		raw, err := json.Marshal(item.Message)
		if err != nil {
			return nil, fmt.Errorf("session: sqlite encode: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO session_items (session_id, idx, item_id, message) VALUES (?, ?, ?, ?)",
			s.id, base+1+i, item.ID, string(raw),
		); err != nil {
			return nil, fmt.Errorf("session: sqlite append: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("session: sqlite append: %w", err)
	}
	return cloneSessionItems(items), nil
}

func (s *sqliteSession) Replace(ctx context.Context, firstID, lastID string, with []model.Message) ([]agent.SessionItem, error) {
	items, err := newSessionItems(with)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("session: sqlite replace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		"SELECT item_id, message FROM session_items WHERE session_id = ? ORDER BY idx ASC", s.id)
	if err != nil {
		return nil, fmt.Errorf("session: sqlite replace: %w", err)
	}
	stored, scanErr := scanSQLiteItems(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return nil, scanErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("session: sqlite replace: %w", closeErr)
	}
	first, last, err := replacementBounds(stored, firstID, lastID)
	if err != nil {
		return nil, err
	}
	updated := make([]agent.SessionItem, 0, len(stored)-(last-first+1)+len(items))
	updated = append(updated, stored[:first]...)
	updated = append(updated, items...)
	updated = append(updated, stored[last+1:]...)
	if _, err := tx.ExecContext(ctx, "DELETE FROM session_items WHERE session_id = ?", s.id); err != nil {
		return nil, fmt.Errorf("session: sqlite replace: %w", err)
	}
	for i, item := range updated {
		raw, err := json.Marshal(item.Message)
		if err != nil {
			return nil, fmt.Errorf("session: sqlite encode: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO session_items (session_id, idx, item_id, message) VALUES (?, ?, ?, ?)",
			s.id, i, item.ID, string(raw),
		); err != nil {
			return nil, fmt.Errorf("session: sqlite replace: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("session: sqlite replace: %w", err)
	}
	return cloneSessionItems(items), nil
}

func (s *sqliteSession) Clear(ctx context.Context) error {
	if _, err := s.store.db.ExecContext(ctx,
		"DELETE FROM session_items WHERE session_id = ?", s.id,
	); err != nil {
		return fmt.Errorf("session: sqlite clear: %w", err)
	}
	return nil
}
