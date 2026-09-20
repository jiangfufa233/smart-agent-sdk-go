package session

import (
	"context"
	"sync"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

// InMemory is a Session backed by a slice, guarded by a mutex. Messages and
// returned items are copied so later caller mutations do not affect stored
// history.
type InMemory struct {
	mu    sync.Mutex
	items []agent.SessionItem
}

// NewInMemory returns an empty in-memory session.
func NewInMemory() *InMemory {
	return &InMemory{}
}

func (m *InMemory) List(ctx context.Context, limit int) ([]agent.SessionItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return cloneSessionItems(lastNItems(m.items, limit)), nil
}

func (m *InMemory) Append(ctx context.Context, messages []model.Message) ([]agent.SessionItem, error) {
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.items = append(m.items, items...)
	return cloneSessionItems(items), nil
}

func (m *InMemory) Replace(ctx context.Context, firstID, lastID string, with []model.Message) ([]agent.SessionItem, error) {
	items, err := newSessionItems(with)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	first, last, err := replacementBounds(m.items, firstID, lastID)
	if err != nil {
		return nil, err
	}
	updated := make([]agent.SessionItem, 0, len(m.items)-(last-first+1)+len(items))
	updated = append(updated, m.items[:first]...)
	updated = append(updated, items...)
	updated = append(updated, m.items[last+1:]...)
	m.items = updated
	return cloneSessionItems(items), nil
}

func (m *InMemory) Clear(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.items = nil
	return nil
}
