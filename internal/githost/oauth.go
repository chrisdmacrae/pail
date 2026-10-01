package githost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrNoOAuth = errors.New("no OAuth app is set up for the git host")

// App is an OAuth app registered with a git host, so people can sign in
// rather than paste a token.
type App struct {
	ClientID     string
	ClientSecret string
	// Server is the host's address when it is one you run: the app is
	// registered there.
	Server string
}

func (a App) Configured() bool { return a.ClientID != "" && a.ClientSecret != "" }

// oauthEnds is where a host's OAuth lives and what to ask it for.
type oauthEnds struct {
	authorize, token string
	// scope is what Pail needs: to read repos and add webhooks.
	scope string
	// basic says the host wants the app's credentials as HTTP basic auth
	// rather than in the form.
	basic bool
}

func oauthFor(kind Kind, server string) oauthEnds {
	server = strings.TrimRight(server, "/")
	switch kind {
	case GitHub:
		if server == "" {
			server = "https://github.com"
		}
		return oauthEnds{authorize: server + "/login/oauth/authorize", token: server + "/login/oauth/access_token", scope: "repo"}
	case GitLab:
		if server == "" {
			server = GitLab.DefaultServer()
		}
		return oauthEnds{authorize: server + "/oauth/authorize", token: server + "/oauth/token", scope: "api"}
	case Bitbucket:
		if server == "" {
			server = "https://bitbucket.org"
		}
		// Bitbucket sets an app's scopes where the app is registered.
		return oauthEnds{authorize: server + "/site/oauth2/authorize", token: server + "/site/oauth2/access_token", basic: true}
	}
	// Gitea and Forgejo.
	return oauthEnds{authorize: server + "/login/oauth/authorize", token: server + "/login/oauth/access_token"}
}

// AuthorizeURL is where to send someone's browser to sign in. The host sends
// them back to redirectURI with a code and the same state.
func AuthorizeURL(kind Kind, app App, redirectURI, state string) string {
	ends := oauthFor(kind, app.Server)
	q := url.Values{
		"client_id":     {app.ClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"state":         {state},
	}
	if ends.scope != "" {
		q.Set("scope", ends.scope)
	}
	return ends.authorize + "?" + q.Encode()
}

// grant is what a host hands back for a code or a refresh token.
type grant struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// ExpiresIn is the access token's life in seconds, or 0 if it doesn't end.
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// redeem trades a code, or a refresh token, for an access token.
func redeem(ctx context.Context, hc *http.Client, kind Kind, app App, form url.Values) (grant, error) {
	ends := oauthFor(kind, app.Server)
	if !ends.basic {
		form.Set("client_id", app.ClientID)
		form.Set("client_secret", app.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ends.token, strings.NewReader(form.Encode()))
	if err != nil {
		return grant{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Without this GitHub answers as a form rather than JSON.
	req.Header.Set("Accept", "application/json")
	if ends.basic {
		req.SetBasicAuth(app.ClientID, app.ClientSecret)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return grant{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var g grant
	if json.Unmarshal(body, &g) != nil || g.AccessToken == "" {
		why := g.ErrorDescription
		if why == "" {
			why = g.Error
		}
		if why == "" {
			why = resp.Status
		}
		return grant{}, fmt.Errorf("%s wouldn’t hand over a token: %s", kind.Label(), why)
	}
	return g, nil
}

// expiry turns a grant's life into the moment it ends; zero if it doesn't.
func (g grant) expiry(now time.Time) time.Time {
	if g.ExpiresIn <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(g.ExpiresIn) * time.Second)
}
