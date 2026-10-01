package storage

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
)

// Memory is an in-process Store for tests.
type Memory struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func NewMemory() *Memory {
	return &Memory{objects: map[string][]byte{}}
}

func (m *Memory) Put(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = b
	return nil
}

func (m *Memory) Read(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	return bytes.Clone(b), nil
}

type memObject struct{ *bytes.Reader }

func (memObject) Close() error { return nil }

func (m *Memory) Open(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	b, err := m.Read(ctx, key)
	if err != nil {
		return nil, err
	}
	return memObject{bytes.NewReader(b)}, nil
}

func (m *Memory) Copy(_ context.Context, src, dst string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[src]
	if !ok {
		return ErrNotFound
	}
	m.objects[dst] = b
	return nil
}

func (m *Memory) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (m *Memory) DeletePrefix(_ context.Context, prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			delete(m.objects, k)
		}
	}
	return nil
}
