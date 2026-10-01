package githost

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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

// Connection is a token for a git host, and whose it is. A pail from a git
// host has one of its own, so two pails can reach their repos with
// different access.
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

// A connection made on New pail waits this long for its pail to be made.
const waitWindow = time.Hour

// waiting is a connection whose pail isn't made yet.
type waiting struct {
	conn  Connection
	since time.Time
}

// slot is where one connection is kept: with a pail, waiting for one under
// an id, or, with neither, as the one every pail of a host once shared.
type slot struct {
	pail string
	id   string
	kind Kind
}

// Connections keeps the git host connections of Pail's pails in the object
// store, beside everything else it stores.
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

	mu sync.RWMutex
	// pails are the connections pails pull with, by pail.
	pails map[string]Connection
	// waiting are the connections made on New pail whose pails aren't made
	// yet, by id. They are never stored: one whose pail is never made is
	// forgotten.
	waiting map[string]waiting
	// shared are from when Pail kept one connection per host for every pail.
	// A pail made then, with none of its own, still pulls with its host's.
	shared map[Kind]Connection
}

const pailKeys = "git/pails/"

func pailKey(pail string) string { return pailKeys + pail + ".json" }
func sharedKey(kind Kind) string { return "git/" + string(kind) + ".json" }
func (at slot) stored() (string, bool) {
	switch {
	case at.pail != "":
		return pailKey(at.pail), true
	case at.id != "":
		return "", false
	}
	return sharedKey(at.kind), true
}

// LoadConnections reads the connections Pail already has.
func LoadConnections(ctx context.Context, store storage.Store, hc *http.Client) (*Connections, error) {
	c := &Connections{store: store, hc: hc, pails: map[string]Connection{}, waiting: map[string]waiting{}, shared: map[Kind]Connection{}}
	read := func(key, whose string) (Connection, bool, error) {
		var conn Connection
		b, err := store.Read(ctx, key)
		if errors.Is(err, storage.ErrNotFound) {
			return conn, false, nil
		}
		if err != nil {
			return conn, false, err
		}
		if err := json.Unmarshal(b, &conn); err != nil {
			return conn, false, fmt.Errorf("the stored %s connection can't be read: %w", whose, err)
		}
		return conn, true, nil
	}
	for _, kind := range Kinds {
		conn, ok, err := read(sharedKey(kind), kind.Label())
		if err != nil {
			return nil, err
		}
		if ok {
			c.shared[kind] = conn
		}
	}
	keys, err := store.List(ctx, pailKeys)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		pail := strings.TrimSuffix(strings.TrimPrefix(key, pailKeys), ".json")
		conn, ok, err := read(key, pail)
		if err != nil {
			return nil, err
		}
		if ok {
			c.pails[pail] = conn
		}
	}
	return c, nil
}

// Connect checks a token with its host and, if the host accepts it, holds it
// for the pail about to be made. It returns the id the connection waits under.
func (c *Connections) Connect(ctx context.Context, kind Kind, server, token string) (string, Connection, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	switch {
	case !kind.SelfHostable():
		server = c.BaseURLs[kind] // normally "": the host's own address
	case server == "" && kind.DefaultServer() == "":
		return "", Connection{}, fmt.Errorf("%w: %s needs its server's address, like https://git.home.example", ErrNoServer, kind.Label())
	}
	return c.hold(ctx, Connection{Kind: kind, Server: server, Token: strings.TrimSpace(token)})
}

// hold asks the host whose token a new connection's is, and has it wait for
// its pail.
func (c *Connections) hold(ctx context.Context, conn Connection) (string, Connection, error) {
	client, err := New(conn.Kind, conn.Server, conn.Token, c.hc)
	if err != nil {
		return "", conn, err
	}
	if conn.Account, err = client.Account(ctx); err != nil {
		return "", conn, err
	}
	var b [16]byte
	rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	c.mu.Lock()
	for old, w := range c.waiting {
		if time.Since(w.since) > waitWindow {
			delete(c.waiting, old)
		}
	}
	c.waiting[id] = waiting{conn: conn, since: time.Now()}
	c.mu.Unlock()
	return id, conn, nil
}

func (c *Connections) get(at slot) (Connection, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch {
	case at.pail != "":
		conn, ok := c.pails[at.pail]
		return conn, ok
	case at.id != "":
		w, ok := c.waiting[at.id]
		return w.conn, ok && time.Since(w.since) <= waitWindow
	}
	conn, ok := c.shared[at.kind]
	return conn, ok
}

func (c *Connections) put(ctx context.Context, at slot, conn Connection) error {
	if key, ok := at.stored(); ok {
		b, err := json.Marshal(conn)
		if err != nil {
			return err
		}
		if err := c.store.Put(ctx, key, bytes.NewReader(b), int64(len(b)), "application/json"); err != nil {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case at.pail != "":
		c.pails[at.pail] = conn
	case at.id != "":
		// Renewing its token doesn't give a connection longer to wait.
		if w, ok := c.waiting[at.id]; ok {
			c.waiting[at.id] = waiting{conn: conn, since: w.since}
		}
	default:
		c.shared[at.kind] = conn
	}
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
// a token, checks it, and holds it for the pail about to be made, as Connect
// does.
func (c *Connections) ConnectOAuth(ctx context.Context, kind Kind, code, redirectURI string) (string, Connection, error) {
	app, err := c.App(kind)
	if err != nil {
		return "", Connection{}, err
	}
	g, err := redeem(ctx, c.hc, kind, app, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
	})
	if err != nil {
		return "", Connection{}, err
	}
	conn := Connection{Kind: kind, Token: g.AccessToken, OAuth: true, Refresh: g.RefreshToken, Expires: g.expiry(time.Now())}
	if kind.SelfHostable() {
		conn.Server = strings.TrimRight(app.Server, "/")
	} else {
		conn.Server = c.BaseURLs[kind]
	}
	return c.hold(ctx, conn)
}

// fresh returns the connection in a slot with a token that still has life in
// it, renewing a signed-in one that is about to run out.
func (c *Connections) fresh(ctx context.Context, at slot) (Connection, error) {
	conn, ok := c.get(at)
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
	if conn, ok = c.get(at); !ok {
		return conn, ErrNotConnected
	}
	if !stale(conn) {
		return conn, nil
	}
	kind := conn.Kind
	app, err := c.App(kind)
	if err != nil || conn.Refresh == "" {
		return conn, fmt.Errorf("%w: the sign-in to %s has run out, and Pail can't renew it", ErrUnauthorized, kind.Label())
	}
	g, err := redeem(ctx, c.hc, kind, app, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {conn.Refresh}})
	if err != nil {
		return conn, fmt.Errorf("%w: the sign-in to %s has run out, and Pail couldn't renew it: %v", ErrUnauthorized, kind.Label(), err)
	}
	conn.Token, conn.Expires = g.AccessToken, g.expiry(time.Now())
	if g.RefreshToken != "" {
		conn.Refresh = g.RefreshToken
	}
	return conn, c.put(ctx, at, conn)
}

func (c *Connections) client(ctx context.Context, at slot) (Client, error) {
	conn, err := c.fresh(ctx, at)
	if err != nil {
		return nil, err
	}
	return New(conn.Kind, conn.Server, conn.Token, c.hc)
}

// Waiting returns the connection waiting under an id for its pail to be
// made, if it is one to this kind of host.
func (c *Connections) Waiting(kind Kind, id string) (Connection, bool) {
	if id == "" {
		return Connection{}, false
	}
	conn, ok := c.get(slot{id: id})
	return conn, ok && conn.Kind == kind
}

// WaitingClient returns a client for a connection whose pail isn't made yet,
// or ErrNotConnected.
func (c *Connections) WaitingClient(ctx context.Context, kind Kind, id string) (Client, error) {
	if _, ok := c.Waiting(kind, id); !ok {
		return nil, ErrNotConnected
	}
	return c.client(ctx, slot{id: id})
}

// Give makes a waiting connection a pail's own. It waits no longer: the
// next pail needs a connection of its own.
func (c *Connections) Give(ctx context.Context, id, pail string) error {
	conn, ok := c.get(slot{id: id})
	if !ok {
		return ErrNotConnected
	}
	if err := c.put(ctx, slot{pail: pail}, conn); err != nil {
		return err
	}
	c.Drop(id)
	return nil
}

// Drop forgets a connection that is waiting for a pail.
func (c *Connections) Drop(id string) {
	c.mu.Lock()
	delete(c.waiting, id)
	c.mu.Unlock()
}

// Forget drops a pail's connection, when the pail goes.
func (c *Connections) Forget(ctx context.Context, pail string) error {
	c.mu.Lock()
	delete(c.pails, pail)
	c.mu.Unlock()
	return c.store.DeletePrefix(ctx, pailKey(pail))
}

// Client returns a client for the host a pail deploys from, or
// ErrNotConnected. It uses the pail's own connection. A pail made when every
// pail of a host shared one connection has none, and uses that one.
func (c *Connections) Client(ctx context.Context, pail string, kind Kind) (Client, error) {
	at := slot{pail: pail}
	if conn, ok := c.get(at); !ok || conn.Kind != kind {
		at = slot{kind: kind}
	}
	return c.client(ctx, at)
}
