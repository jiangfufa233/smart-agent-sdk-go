package session

import (
	"context"
	"sync"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

// InMemory is a Session backed by a slice, guarded by a mutex. Messages are
// copied on AddItems and GetItems, so later caller mutations do not affect
// the stored history.
type InMemory struct {
	mu   sync.Mutex
	msgs []model.Message
}

// NewInMemory returns an empty in-memory session.
func NewInMemory() *InMemory {
	return &InMemory{}
}

func (m *InMemory) GetItems(ctx context.Context, limit int) ([]model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneMessages(lastN(m.msgs, limit)), nil
}

func (m *InMemory) AddItems(ctx context.Context, items []model.Message) error {
	if len(items) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, cloneMessages(items)...)
	return nil
}

func (m *InMemory) Clear(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = nil
	return nil
}
