package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

// FileSession persists messages as JSONL (one model.Message JSON object per
// line), so a conversation survives process restarts. AddItems appends,
// GetItems reads the file and returns the most recent limit messages, Clear
// truncates the file. A missing file reads as an empty session.
type FileSession struct {
	mu   sync.Mutex
	path string
}

// NewFile returns a session backed by the JSONL file at path.
func NewFile(path string) *FileSession {
	return &FileSession{path: path}
}

func (f *FileSession) GetItems(ctx context.Context, limit int) ([]model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: read %s: %w", f.path, err)
	}
	var msgs []model.Message
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m model.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, fmt.Errorf("session: %s line %d: %w", f.path, i+1, err)
		}
		msgs = append(msgs, m)
	}
	return lastN(msgs, limit), nil
}

func (f *FileSession) AddItems(ctx context.Context, items []model.Message) error {
	if len(items) == 0 {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, m := range items {
		if err := enc.Encode(m); err != nil {
			return fmt.Errorf("session: encode message: %w", err)
		}
	}
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("session: open %s: %w", f.path, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("session: append %s: %w", f.path, err)
	}
	return nil
}

func (f *FileSession) Clear(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.Truncate(f.path, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("session: clear %s: %w", f.path, err)
	}
	return nil
}
