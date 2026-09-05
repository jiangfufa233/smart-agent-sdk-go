package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/model"
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
	message    TEXT NOT NULL,
	PRIMARY KEY (session_id, idx)
)`

// NewSQLiteStore opens (creating if needed) the database at path and
// ensures the schema exists. Call Close when done.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
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
	return &SQLiteStore{db: db}, nil
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

func (s *sqliteSession) GetItems(ctx context.Context, limit int) ([]model.Message, error) {
	q := "SELECT message FROM session_items WHERE session_id = ? ORDER BY idx ASC"
	args := []any{s.id}
	if limit > 0 {
		q = "SELECT message FROM session_items WHERE session_id = ? ORDER BY idx DESC LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.store.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("session: sqlite get: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var msgs []model.Message
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("session: sqlite scan: %w", err)
		}
		var m model.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("session: sqlite decode: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("session: sqlite get: %w", err)
	}
	if limit > 0 {
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
	}
	return msgs, nil
}

func (s *sqliteSession) AddItems(ctx context.Context, items []model.Message) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("session: sqlite add: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var base int
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(idx), -1) FROM session_items WHERE session_id = ?", s.id,
	).Scan(&base); err != nil {
		return fmt.Errorf("session: sqlite add: %w", err)
	}
	for i, m := range items {
		raw, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("session: sqlite encode: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO session_items (session_id, idx, message) VALUES (?, ?, ?)",
			s.id, base+1+i, string(raw),
		); err != nil {
			return fmt.Errorf("session: sqlite add: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("session: sqlite add: %w", err)
	}
	return nil
}

func (s *sqliteSession) Clear(ctx context.Context) error {
	if _, err := s.store.db.ExecContext(ctx,
		"DELETE FROM session_items WHERE session_id = ?", s.id,
	); err != nil {
		return fmt.Errorf("session: sqlite clear: %w", err)
	}
	return nil
}
