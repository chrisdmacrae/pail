package githost

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// Push is what a webhook delivery turned out to be.
type Push struct {
	// Genuine says the delivery was signed with the hook's secret.
	Genuine bool
	// Branch is the branch that was pushed to, or "" when the delivery
	// wasn't a push to a branch (a ping, a tag).
	Branch string
}

func signed(secret string, body []byte, got string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(strings.TrimPrefix(got, "sha256="))) == 1
}

// ReadPush checks a webhook delivery from a host against the hook's secret
// and reads which branch it's about. Each host signs in its own way.
func ReadPush(kind Kind, secret string, header http.Header, body []byte) Push {
	var p Push
	switch kind {
	case GitHub:
		p.Genuine = signed(secret, body, header.Get("X-Hub-Signature-256"))
	case GitLab:
		// GitLab sends the secret itself rather than a signature.
		p.Genuine = subtle.ConstantTimeCompare([]byte(header.Get("X-Gitlab-Token")), []byte(secret)) == 1
	case Gitea, Forgejo:
		p.Genuine = signed(secret, body, header.Get("X-Gitea-Signature")) || signed(secret, body, header.Get("X-Hub-Signature-256"))
	case Bitbucket:
		p.Genuine = signed(secret, body, header.Get("X-Hub-Signature"))
	}
	if !p.Genuine {
		return p
	}

	if kind == Bitbucket {
		var payload struct {
			Push struct {
				Changes []struct {
					New *struct{ Type, Name string } `json:"new"`
				} `json:"changes"`
			} `json:"push"`
		}
		if json.Unmarshal(body, &payload) == nil {
			for _, c := range payload.Push.Changes {
				if c.New != nil && c.New.Type == "branch" {
					p.Branch = c.New.Name
				}
			}
		}
		return p
	}
	var payload struct {
		Ref string `json:"ref"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if branch, ok := strings.CutPrefix(payload.Ref, "refs/heads/"); ok {
			p.Branch = branch
		}
	}
	return p
}
