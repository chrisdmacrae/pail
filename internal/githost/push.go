package githost

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path"
	"strings"
)

// Push is what a webhook delivery turned out to be.
type Push struct {
	// Genuine says the delivery was signed with the hook's secret.
	Genuine bool
	// Branch is the branch that was pushed to, or "" when the delivery
	// wasn't a push to a branch (a ping, a tag).
	Branch string
	// Commit is the commit the push left the branch at, or "" when the
	// delivery doesn't say. Pulling it rather than the branch gets what was
	// pushed even from a host that is still catching up with the push.
	Commit string
	// Changed lists the files the push added, changed or removed, as paths
	// from the top of the repo. It is nil when the delivery doesn't say, or
	// can't be trusted to say it all: what was pushed may then be anything.
	Changed []string
}

// manyCommits is how many commits a delivery may list before Pail takes the
// list to be cut short: hosts list the first twenty or so of a long push.
const manyCommits = 20

// workspaceFiles are the files above a project that its build reads when it
// is part of a workspace.
var workspaceFiles = map[string]bool{
	"package.json": true, "pnpm-lock.yaml": true, "package-lock.json": true, "yarn.lock": true,
	"pnpm-workspace.yaml": true, ".npmrc": true,
}

// Touches reports whether the push changed a pail that is the folder dir of
// its repo: a file in the folder, a file in one of the folders or files watch
// names, or one of a workspace's own files in a folder above. A push that
// doesn't say what it changed touches every pail, and so does any push for a
// pail that is the whole repo.
func (p Push) Touches(dir string, watch []string) bool {
	if dir == "" || p.Changed == nil {
		return true
	}
	within := func(file, folder string) bool {
		return folder == "" || file == folder || strings.HasPrefix(file, folder+"/")
	}
	for _, file := range p.Changed {
		if within(file, dir) {
			return true
		}
		for _, w := range watch {
			if within(file, w) {
				return true
			}
		}
		above := path.Dir(file)
		if above == "." {
			above = ""
		}
		if workspaceFiles[path.Base(file)] && within(dir, above) {
			return true
		}
	}
	return false
}

// commit cleans the commit a delivery names. A host names a branch that was
// deleted by a commit of all zeros, which is no commit.
func commit(sha string) string {
	if strings.Trim(sha, "0") == "" {
		return ""
	}
	return sha
}

func signed(secret string, body []byte, got string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(strings.TrimPrefix(got, "sha256="))) == 1
}

// ReadPush checks a webhook delivery from a host against the hook's secret
// and reads which branch it's about, and which files where the host says.
// Each host signs in its own way. Bitbucket doesn't list files.
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
					New *struct {
						Type, Name string
						Target     struct{ Hash string }
					} `json:"new"`
				} `json:"changes"`
			} `json:"push"`
		}
		if json.Unmarshal(body, &payload) == nil {
			for _, c := range payload.Push.Changes {
				if c.New != nil && c.New.Type == "branch" {
					p.Branch, p.Commit = c.New.Name, commit(c.New.Target.Hash)
				}
			}
		}
		return p
	}
	var payload struct {
		Ref string `json:"ref"`
		// The commit the branch is at now: all zeros when it was deleted.
		After string `json:"after"`
		// GitHub says when a push made the branch, or rewrote it.
		Created bool `json:"created"`
		Forced  bool `json:"forced"`
		Commits []struct {
			Added    *[]string `json:"added"`
			Modified *[]string `json:"modified"`
			Removed  *[]string `json:"removed"`
		} `json:"commits"`
		// How many commits were pushed, where commits may list fewer:
		// Gitea and Forgejo's name for it, and GitLab's.
		Total      *int `json:"total_commits"`
		TotalCount *int `json:"total_commits_count"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return p
	}
	if branch, ok := strings.CutPrefix(payload.Ref, "refs/heads/"); ok {
		p.Branch, p.Commit = branch, commit(payload.After)
	}

	// The files are known only when every commit of the push is listed, with
	// its files. Anything less and the push may have changed anything.
	n := len(payload.Commits)
	switch {
	case n == 0 || n >= manyCommits || payload.Created || payload.Forced:
		return p
	case payload.Total != nil && *payload.Total != n, payload.TotalCount != nil && *payload.TotalCount != n:
		return p
	}
	changed := []string{}
	for _, c := range payload.Commits {
		if c.Added == nil || c.Modified == nil || c.Removed == nil {
			return p
		}
		changed = append(changed, *c.Added...)
		changed = append(changed, *c.Modified...)
		changed = append(changed, *c.Removed...)
	}
	p.Changed = changed
	return p
}
