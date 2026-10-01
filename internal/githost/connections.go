package githost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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
	// OAuth says the token came from signing in rather than being pasted.
	// Such a token may run out; Refresh gets the next one, and Expires is
	// when this one ends (zero if it doesn't).
	OAuth   bool      `json:"oauth,omitempty"`
	Refresh string    `json:"refresh,omitempty"`
	Expires time.Time `json:"expires,omitzero"`
}

// Connections keeps Pail's git host connections in the object store, beside
// everything else it stores.
type Connections struct {
	store storage.Store
	hc    *http.Client
	// BaseURLs says where a host that isn't self-hosted lives, in place of
	// its real address. It is for tests.
	BaseURLs map[Kind]string
	// Apps are the OAuth apps set up on the server, by host.
	Apps map[Kind]App

	// refreshing lets one token be renewed at a time: a refresh token works once.
	refreshing sync.Mutex

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
	return conn, c.save(ctx, conn)
}

func (c *Connections) save(ctx context.Context, conn Connection) error {
	b, err := json.Marshal(conn)
	if err != nil {
		return err
	}
	if err := c.store.Put(ctx, connectionKey(conn.Kind), bytes.NewReader(b), int64(len(b)), "application/json"); err != nil {
		return err
	}
	c.mu.Lock()
	c.all[conn.Kind] = conn
	c.mu.Unlock()
	return nil
}

// App returns the OAuth app set up for a host, or ErrNoOAuth.
func (c *Connections) App(kind Kind) (App, error) {
	app := c.Apps[kind]
	if !app.Configured() {
		return app, ErrNoOAuth
	}
	return app, nil
}

// ConnectOAuth finishes a sign-in: it trades the code the host sent back for
// a token, checks it, and keeps it.
func (c *Connections) ConnectOAuth(ctx context.Context, kind Kind, code, redirectURI string) (Connection, error) {
	app, err := c.App(kind)
	if err != nil {
		return Connection{}, err
	}
	g, err := redeem(ctx, c.hc, kind, app, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
	})
	if err != nil {
		return Connection{}, err
	}
	conn := Connection{Kind: kind, Token: g.AccessToken, OAuth: true, Refresh: g.RefreshToken, Expires: g.expiry(time.Now())}
	if kind.SelfHostable() {
		conn.Server = strings.TrimRight(app.Server, "/")
	} else {
		conn.Server = c.BaseURLs[kind]
	}
	client, err := New(kind, conn.Server, conn.Token, c.hc)
	if err != nil {
		return conn, err
	}
	if conn.Account, err = client.Account(ctx); err != nil {
		return conn, err
	}
	return conn, c.save(ctx, conn)
}

// fresh returns a connection whose token still has life in it, renewing a
// signed-in one that is about to run out.
func (c *Connections) fresh(ctx context.Context, kind Kind) (Connection, error) {
	conn, ok := c.Get(kind)
	if !ok {
		return conn, ErrNotConnected
	}
	stale := func(conn Connection) bool {
		return conn.OAuth && !conn.Expires.IsZero() && time.Until(conn.Expires) < time.Minute
	}
	if !stale(conn) {
		return conn, nil
	}

	c.refreshing.Lock()
	defer c.refreshing.Unlock()
	// Someone else may have renewed it while this waited.
	if conn, ok = c.Get(kind); !ok {
		return conn, ErrNotConnected
	}
	if !stale(conn) {
		return conn, nil
	}
	app, err := c.App(kind)
	if err != nil || conn.Refresh == "" {
		return conn, fmt.Errorf("%w: the sign-in to %s has run out. Sign in again from New pail", ErrUnauthorized, kind.Label())
	}
	g, err := redeem(ctx, c.hc, kind, app, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {conn.Refresh}})
	if err != nil {
		return conn, fmt.Errorf("%w: %v. Sign in again from New pail", ErrUnauthorized, err)
	}
	conn.Token, conn.Expires = g.AccessToken, g.expiry(time.Now())
	if g.RefreshToken != "" {
		conn.Refresh = g.RefreshToken
	}
	return conn, c.save(ctx, conn)
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
func (c *Connections) Client(ctx context.Context, kind Kind) (Client, error) {
	conn, err := c.fresh(ctx, kind)
	if err != nil {
		return nil, err
	}
	return New(kind, conn.Server, conn.Token, c.hc)
}
