package githost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/chrisdmacrae/pail/internal/storage"
)

var (
	ErrNotConnected = errors.New("the git host isn't connected")
	ErrNoServer     = errors.New("no server address")
)

// Connection is one git host Pail has a token for. There is one per kind of
// host: Pail has no users for a second to belong to.
type Connection struct {
	Kind Kind `json:"kind"`
	// Server is the host's address when it is one you run, else "".
	Server string `json:"server,omitempty"`
	Token  string `json:"token"`
	// Account is whose token it is, when the host says.
	Account string `json:"account,omitempty"`
}

// Connections keeps Pail's git host connections in the object store, beside
// everything else it stores.
type Connections struct {
	store storage.Store
	hc    *http.Client
	// BaseURLs says where a host that isn't self-hosted lives, in place of
	// its real address. It is for tests.
	BaseURLs map[Kind]string

	mu  sync.RWMutex
	all map[Kind]Connection
}

func connectionKey(kind Kind) string { return "git/" + string(kind) + ".json" }

// LoadConnections reads the connections Pail already has.
func LoadConnections(ctx context.Context, store storage.Store, hc *http.Client) (*Connections, error) {
	c := &Connections{store: store, hc: hc, all: map[Kind]Connection{}}
	for _, kind := range Kinds {
		b, err := store.Read(ctx, connectionKey(kind))
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var conn Connection
		if err := json.Unmarshal(b, &conn); err != nil {
			return nil, fmt.Errorf("the stored %s connection can't be read: %w", kind.Label(), err)
		}
		c.all[kind] = conn
	}
	return c, nil
}

// Connect checks a token with its host and, if the host accepts it, keeps it.
func (c *Connections) Connect(ctx context.Context, kind Kind, server, token string) (Connection, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	switch {
	case !kind.SelfHostable():
		server = c.BaseURLs[kind] // normally "": the host's own address
	case server == "" && kind.DefaultServer() == "":
		return Connection{}, fmt.Errorf("%w: %s needs its server's address, like https://git.home.example", ErrNoServer, kind.Label())
	}
	conn := Connection{Kind: kind, Server: server, Token: strings.TrimSpace(token)}
	client, err := New(kind, server, conn.Token, c.hc)
	if err != nil {
		return conn, err
	}
	if conn.Account, err = client.Account(ctx); err != nil {
		return conn, err
	}
	b, err := json.Marshal(conn)
	if err != nil {
		return conn, err
	}
	if err := c.store.Put(ctx, connectionKey(kind), bytes.NewReader(b), int64(len(b)), "application/json"); err != nil {
		return conn, err
	}
	c.mu.Lock()
	c.all[kind] = conn
	c.mu.Unlock()
	return conn, nil
}

// Disconnect forgets a host's token.
func (c *Connections) Disconnect(ctx context.Context, kind Kind) error {
	c.mu.Lock()
	delete(c.all, kind)
	c.mu.Unlock()
	return c.store.DeletePrefix(ctx, connectionKey(kind))
}

func (c *Connections) Get(kind Kind) (Connection, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	conn, ok := c.all[kind]
	return conn, ok
}

// Client returns a client for a connected host, or ErrNotConnected.
func (c *Connections) Client(kind Kind) (Client, error) {
	conn, ok := c.Get(kind)
	if !ok {
		return nil, ErrNotConnected
	}
	return New(kind, conn.Server, conn.Token, c.hc)
}
