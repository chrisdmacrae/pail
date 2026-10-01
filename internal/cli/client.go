package cli

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// What the API sends back. Only the fields the CLI uses.
type apiInfo struct {
	Version    string `json:"version"`
	BaseDomain string `json:"base_domain"`
	TLS        string `json:"tls"`
	Limits     struct {
		MaxUploadSize int64 `json:"max_upload_size"`
	} `json:"limits"`
}

type apiPail struct {
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	URL       string     `json:"url"`
	Serving   string     `json:"serving"`
	UpdatedAt time.Time  `json:"updated_at"`
	Deploy    *apiDeploy `json:"deploy"`
}

type apiDeploy struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Error      string     `json:"error"`
	URL        string     `json:"url"`
	Serving    bool       `json:"serving"`
}

type apiHost struct {
	Host       string `json:"host"`
	URL        string `json:"url"`
	Default    bool   `json:"default"`
	PointsHere bool   `json:"points_here"`
	Detail     string `json:"detail"`
}

type apiLine struct {
	Time  time.Time `json:"time"`
	Text  string    `json:"text"`
	Level string    `json:"level,omitempty"`
	// Source is the container that printed the line, in a pail's output.
	Source string `json:"source,omitempty"`
}

// client talks to one Pail installation's REST API.
type client struct {
	target target
	http   *http.Client
}

func (a *app) newClient(t target) (*client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Long enough for the server to get a hostname its certificate, which
	// waits for DNS.
	transport.ResponseHeaderTimeout = 5 * time.Minute
	if t.CA != "" {
		path := a.expandHome(t.CA)
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, usagef("Can't read the certificate for %s at %s: %v.", t.name, a.shortPath(path), err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, usagef("%s isn't a PEM certificate.", a.shortPath(path))
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return &client{target: t, http: &http.Client{Transport: transport}}, nil
}

// do sends one request and turns anything but a 2xx into an exitError with
// the right exit code.
func (c *client) do(method, path string, body io.Reader, size int64, contentType string) (*http.Response, error) {
	req, err := http.NewRequest(method, c.target.URL+path, body)
	if err != nil {
		return nil, usagef("%s isn't a URL Pail can use: %v.", c.target.URL, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.target.Token)
	if body != nil {
		req.ContentLength = size
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if untrusted(err) {
		return nil, &exitError{code: ExitUnreachable, cause: err, msg: fmt.Sprintf("%s uses a certificate this machine doesn't trust. Run pail login %s to trust that installation's own authority, or set PAIL_CA to its root certificate.", c.target.URL, c.target.URL)}
	}
	if err != nil {
		return nil, &exitError{code: ExitUnreachable, cause: err, msg: fmt.Sprintf("Can't reach %s: %v. Check that Pail is running there.", c.target.URL, unwrapURLError(err))}
	}
	if resp.StatusCode < 400 {
		return resp, nil
	}
	defer resp.Body.Close()

	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if json.Unmarshal(raw, &e) != nil || e.Error.Message == "" {
		return nil, &exitError{code: ExitUnreachable, msg: fmt.Sprintf("%s answered %s, which isn't Pail's API. Check the URL.", c.target.URL, resp.Status)}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		fix := "Run pail login " + c.target.URL + " with the right one."
		if c.target.name == "PAIL_URL" {
			fix = "Check PAIL_TOKEN."
		}
		return nil, &exitError{code: ExitTokenRejected, msg: fmt.Sprintf("%s rejected the token. %s", c.target.URL, fix)}
	case resp.StatusCode == http.StatusNotFound:
		return nil, &exitError{code: ExitNotFound, msg: e.Error.Message}
	case resp.StatusCode >= 500:
		return nil, &exitError{code: ExitDeployFailed, msg: e.Error.Message}
	}
	return nil, &exitError{code: ExitUsage, msg: e.Error.Message}
}

func (c *client) get(path string, v any) error {
	resp, err := c.do("GET", path, nil, 0, "")
	if err != nil {
		return err
	}
	return c.decode(resp, v)
}

// post sends in as JSON, if there is one, and reads the answer into out.
func (c *client) post(path string, in, out any) error {
	var body io.Reader
	var size int64
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, size = bytes.NewReader(b), int64(len(b))
	}
	resp, err := c.do("POST", path, body, size, "application/json")
	if err != nil {
		return err
	}
	return c.decode(resp, out)
}

func (c *client) decode(resp *http.Response, v any) error {
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return &exitError{code: ExitUnreachable, msg: fmt.Sprintf("%s answered, but not as Pail's API. Check the URL.", c.target.URL)}
	}
	return nil
}

// streamLog reads a deploy's log, calling line for each one. It returns the
// finished deploy, or nil if the stream ended while the deploy was still
// building.
func (c *client) streamLog(pail, deploy string, follow bool, line func(apiLine)) (*apiDeploy, error) {
	path := "/api/v1/pails/" + pail + "/deploys/" + deploy + "/log"
	if !follow {
		path += "?follow=false"
	}
	resp, err := c.do("GET", path, nil, 0, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	event := ""
	for sc.Scan() {
		field, value, _ := strings.Cut(sc.Text(), ": ")
		switch {
		case field == "event":
			event = value
		case field == "data" && event == "line":
			var l apiLine
			if json.Unmarshal([]byte(value), &l) == nil {
				line(l)
			}
		case field == "data" && event == "done":
			var d apiDeploy
			if err := json.Unmarshal([]byte(value), &d); err != nil {
				return nil, err
			}
			return &d, nil
		}
	}
	if err := sc.Err(); err != nil {
		return nil, &exitError{code: ExitUnreachable, msg: fmt.Sprintf("Lost %s partway through the log: %v. The deploy carries on; run pail logs %s.", c.target.URL, err, pail)}
	}
	if follow {
		return nil, &exitError{code: ExitUnreachable, msg: fmt.Sprintf("%s closed the log early. The deploy carries on; run pail logs %s.", c.target.URL, pail)}
	}
	return nil, nil
}

// streamOutput reads what a pail's containers print: the lines Pail has
// kept and, with follow, each new one until the caller stops it.
func (c *client) streamOutput(pail string, follow bool, line func(apiLine)) error {
	path := "/api/v1/pails/" + pail + "/output"
	if !follow {
		path += "?follow=false"
	}
	resp, err := c.do("GET", path, nil, 0, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		if value, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var l apiLine
			if json.Unmarshal([]byte(value), &l) == nil {
				line(l)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return &exitError{code: ExitUnreachable, msg: fmt.Sprintf("Lost %s partway through: %v. Run pail logs %s --output again.", c.target.URL, err, pail)}
	}
	return nil
}

// unwrapURLError drops the "Get https://...:" prefix net/http adds; the
// message already names the installation.
func unwrapURLError(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok && u.Unwrap() != nil {
		return u.Unwrap()
	}
	return err
}
