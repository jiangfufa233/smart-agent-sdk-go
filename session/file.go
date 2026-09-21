package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model"
)

const fileSessionFormatVersion = 2

// FileSession persists versioned session items as JSONL, so a conversation
// survives process restarts. Every item line carries the v2 format marker,
// generated item ID, and model message. Mutations rewrite a complete file via
// an atomic rename so a turn or replacement is never partially persisted.
type FileSession struct {
	mu   sync.Mutex
	path string
}

type fileSessionItem struct {
	Version int           `json:"version"`
	ID      string        `json:"id"`
	Message model.Message `json:"message"`
}

// NewFile returns a session backed by the JSONL file at path.
func NewFile(path string) *FileSession {
	return &FileSession{path: path}
}

func (f *FileSession) List(ctx context.Context, limit int) ([]agent.SessionItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items, err := f.readItems()
	if err != nil {
		return nil, err
	}
	return cloneSessionItems(lastNItems(items, limit)), nil
}

func (f *FileSession) Append(ctx context.Context, messages []model.Message) ([]agent.SessionItem, error) {
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
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stored, err := f.readItems()
	if err != nil {
		return nil, err
	}
	stored = append(stored, items...)
	if err := f.rewriteItems(ctx, stored); err != nil {
		return nil, err
	}
	return cloneSessionItems(items), nil
}

func (f *FileSession) Replace(ctx context.Context, firstID, lastID string, with []model.Message) ([]agent.SessionItem, error) {
	items, err := newSessionItems(with)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stored, err := f.readItems()
	if err != nil {
		return nil, err
	}
	first, last, err := replacementBounds(stored, firstID, lastID)
	if err != nil {
		return nil, err
	}
	updated := make([]agent.SessionItem, 0, len(stored)-(last-first+1)+len(items))
	updated = append(updated, stored[:first]...)
	updated = append(updated, items...)
	updated = append(updated, stored[last+1:]...)
	if err := f.rewriteItems(ctx, updated); err != nil {
		return nil, err
	}
	return cloneSessionItems(items), nil
}

func (f *FileSession) Clear(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := f.readItems(); err != nil {
		return err
	}
	return f.rewriteItems(ctx, nil)
}

func (f *FileSession) readItems() ([]agent.SessionItem, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: read %s: %w", f.path, err)
	}
	var items []agent.SessionItem
	seen := make(map[string]bool)
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item fileSessionItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, fmt.Errorf("session: %s line %d: %w", f.path, i+1, err)
		}
		if item.Version != fileSessionFormatVersion {
			return nil, &FormatUpgradeError{
				Store:  "file",
				Detail: fmt.Sprintf("%s line %d uses legacy or unsupported JSONL version %d; migrate v1 data before use", f.path, i+1, item.Version),
			}
		}
		if item.ID == "" || seen[item.ID] {
			return nil, fmt.Errorf("session: %s line %d: missing or duplicate item ID", f.path, i+1)
		}
		seen[item.ID] = true
		items = append(items, agent.SessionItem{ID: item.ID, Message: item.Message})
	}
	return items, nil
}

func (f *FileSession) rewriteItems(ctx context.Context, items []agent.SessionItem) (err error) {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, item := range items {
		if err := encoder.Encode(fileSessionItem{
			Version: fileSessionFormatVersion,
			ID:      item.ID,
			Message: item.Message,
		}); err != nil {
			return fmt.Errorf("session: encode item: %w", err)
		}
	}

	dir := filepath.Dir(f.path)
	info, statErr := os.Stat(f.path)
	mode := os.FileMode(0o644)
	if statErr == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("session: stat %s: %w", f.path, statErr)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(f.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("session: create temporary file for %s: %w", f.path, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("session: set temporary file mode for %s: %w", f.path, err)
	}
	if _, err := tmp.Write(data.Bytes()); err != nil {
		return fmt.Errorf("session: write temporary file for %s: %w", f.path, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("session: sync temporary file for %s: %w", f.path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("session: close temporary file for %s: %w", f.path, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, f.path); err != nil {
		return fmt.Errorf("session: replace %s: %w", f.path, err)
	}
	return nil
}
