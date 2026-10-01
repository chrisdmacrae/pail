package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/chrisdmacrae/pail/web"
)

// page is what Pail says when it has no site to serve: what happened, then
// what to do about it. A browser gets it as a page in Pail's own look;
// anything else gets the same words as plain text.
type page struct {
	status int
	// what happened, as one sentence. It is the page's headline.
	what string
	// fix says what to do. When there is a command, fix leads into it:
	// "Start it with".
	fix     string
	command string
	// pill is the word on the status pill, and tone its colour: toneQuiet,
	// toneBuilding or toneFailed. Left empty, both follow from the status.
	pill, tone string
	// retry, in seconds, makes a browser ask again by itself: for what
	// passes without anyone doing anything.
	retry int
}

const (
	toneQuiet    = "quiet"
	toneBuilding = "building"
	toneFailed   = "failed"
)

// pagePolicy lets the page load its own styles and Pail's fonts, and
// nothing else: it is served on a pail's own host.
const pagePolicy = "default-src 'none'; style-src 'unsafe-inline'; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// problem answers a request with a page.
func (s *Server) problem(w http.ResponseWriter, r *http.Request, p page) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "Accept")
	h.Set("X-Content-Type-Options", "nosniff")
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(p.status)
		if r.Method != http.MethodHead {
			fmt.Fprintf(w, "%s\n\nThis is Pail on %s.\n", p.text(), s.cfg.BaseDomain)
		}
		return
	}

	view := pageView{
		Status: p.status, What: p.what, Fix: p.fix, Command: p.command, Pill: p.pill, Tone: p.tone,
		Empty:  p.status == http.StatusNotFound,
		Domain: s.cfg.BaseDomain, Home: origin(r, s.cfg.BaseDomain),
	}
	if view.Pill == "" {
		view.Pill = pills[p.status]
	}
	if view.Pill == "" {
		view.Pill = "Failed"
	}
	if view.Tone == "" {
		view.Tone = toneQuiet
		if p.status >= 500 {
			view.Tone = toneFailed
		}
	}
	// Only a GET is safe to send again unasked.
	if p.retry > 0 && r.Method == http.MethodGet {
		view.Retry, view.Fix, view.Command = p.retry, "This page tries again on its own.", ""
	}
	var out bytes.Buffer
	if err := pageTemplate.Execute(&out, view); err != nil {
		s.log.Error("page", "err", err)
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", pagePolicy)
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(p.status)
	if r.Method != http.MethodHead {
		w.Write(out.Bytes())
	}
}

// text is the page in one line.
func (p page) text() string {
	t := p.what
	if p.fix != "" {
		t += " " + p.fix
	}
	if p.command != "" {
		t += " " + p.command + "."
	}
	return t
}

// pills are the status pill's words, for pages that don't bring their own.
var pills = map[int]string{
	http.StatusNotFound:              "Not found",
	http.StatusMethodNotAllowed:      "Not allowed",
	http.StatusRequestEntityTooLarge: "Too big",
}

type pageView struct {
	Status             int
	What, Fix, Command string
	Pill, Tone         string
	Retry              int
	// Empty draws the pail with nothing in it.
	Empty        bool
	Domain, Home string
}

// fontPath is where every host serves the fonts Pail's pages use. It is
// under the one path Pail keeps for itself on a pail's host.
const fontPath = probePath + "fonts/"

// pageFonts are the design system's fonts, by the names pages ask for.
var pageFonts = map[string][]byte{
	"display.woff2": readFont("bricolage-grotesque-latin-wght-normal.woff2"),
	"sans.woff2":    readFont("hanken-grotesk-latin-wght-normal.woff2"),
	"mono.woff2":    readFont("ibm-plex-mono-latin-500-normal.woff2"),
}

// fontRev names this build's fonts, so a browser can keep them for good.
var fontRev = func() string {
	sum := sha256.New()
	for _, name := range []string{"display.woff2", "sans.woff2", "mono.woff2"} {
		sum.Write(pageFonts[name])
	}
	return hex.EncodeToString(sum.Sum(nil))[:8]
}()

func readFont(name string) []byte {
	data, err := web.Fonts.ReadFile("design-system/fonts/" + name)
	if err != nil {
		panic(err) // the file is compiled in
	}
	return data
}

// serveFont answers for one of the fonts, if name is one.
func (s *Server) serveFont(w http.ResponseWriter, r *http.Request, name string) bool {
	rev, file, _ := strings.Cut(name, "/")
	data, ok := pageFonts[file]
	if !ok || rev != fontRev || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	w.Header().Set("Content-Type", "font/woff2")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	return true
}

var pageTemplate = template.Must(template.New("page").Parse(strings.ReplaceAll(pageHTML, "/FONTS/", fontPath+fontRev+"/")))

// pageHTML is the whole page: the design system's tokens for Day and Night,
// its Status pill and its Command, with nothing to load but the fonts.
const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<meta name="color-scheme" content="light dark">
{{if .Retry}}<meta http-equiv="refresh" content="{{.Retry}}">
{{end}}<title>{{.What}}</title>
<style>
@font-face { font-family: "Bricolage Grotesque"; src: url("/FONTS/display.woff2") format("woff2"); font-weight: 200 800; font-display: swap; }
@font-face { font-family: "Hanken Grotesk"; src: url("/FONTS/sans.woff2") format("woff2"); font-weight: 100 900; font-display: swap; }
@font-face { font-family: "IBM Plex Mono"; src: url("/FONTS/mono.woff2") format("woff2"); font-weight: 400 500; font-display: swap; }
:root {
  --surface: #f5f1e8;
  --surface-sunk: #ebe5d8;
  --line: #dcd4c4;
  --ink: #1e1c17;
  --ink-muted: #5c564b;
  --pail: #f2b134;
  --pail-ink: #865400;
  --pail-soft: #fbecc4;
  --failed: #b02a1f;
  --failed-soft: #fbe0dc;
  --link: #1f5f63;
  --focus-ring: 0 0 0 2px #f5f1e8, 0 0 0 4px #1f5f63;
  --font-display: "Bricolage Grotesque", "Hanken Grotesk", ui-sans-serif, system-ui, sans-serif;
  --font-sans: "Hanken Grotesk", ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
  --font-mono: "IBM Plex Mono", ui-monospace, "SF Mono", Menlo, monospace;
}
@media (prefers-color-scheme: dark) {
  :root {
    --surface: #16150f;
    --surface-sunk: #0e0d09;
    --line: #34312a;
    --ink: #f2ede2;
    --ink-muted: #aca493;
    --pail: #f4bd4c;
    --pail-ink: #f4bd4c;
    --pail-soft: #3a2c0c;
    --failed: #ff8a7a;
    --failed-soft: #3d1712;
    --link: #9fd0cc;
    --focus-ring: 0 0 0 2px #16150f, 0 0 0 4px #9fd0cc;
  }
}
* { box-sizing: border-box; }
html { height: 100%; -webkit-text-size-adjust: 100%; }
body {
  min-height: 100%;
  margin: 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  grid-template-rows: 1fr auto;
  background: var(--surface);
  color: var(--ink);
  font: 400 16px/24px var(--font-sans);
}
main, footer { width: 100%; max-width: 608px; margin: 0 auto; padding: 0 24px; }
main { align-self: center; padding-top: 48px; padding-bottom: 72px; }
.mark { display: block; width: 48px; height: 48px; margin-left: -3px; color: var(--ink); }
.mark .fill { fill: var(--pail); }
.state { display: flex; align-items: center; gap: 12px; margin: 32px 0 0; }
.pill { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 10px 0 8px; border-radius: 9999px; font-size: 12px; font-weight: 600; letter-spacing: .03em; white-space: nowrap; }
.dot { width: 8px; height: 8px; border-radius: 9999px; background: currentColor; }
.quiet { background: var(--surface-sunk); color: var(--ink-muted); }
.quiet .dot { background: transparent; box-shadow: inset 0 0 0 1.5px currentColor; }
.building { background: var(--pail-soft); color: var(--pail-ink); }
.building .dot { background: var(--pail); box-shadow: 0 0 0 2px var(--pail-ink); animation: glow 1.6s ease-in-out infinite; }
.failed { background: var(--failed-soft); color: var(--failed); }
.code { font: 400 13px/24px var(--font-mono); color: var(--ink-muted); }
h1 { margin: 16px 0 0; font: 700 32px/38px var(--font-display); letter-spacing: -0.02em; overflow-wrap: anywhere; text-wrap: balance; }
.fix { margin: 12px 0 0; color: var(--ink-muted); text-wrap: pretty; }
.cmd { margin: 12px 0 0; padding: 12px 16px; background: var(--surface-sunk); border-radius: 4px; font: 500 15px/24px var(--font-mono); white-space: pre; overflow-x: auto; }
.cmd:focus-visible, a:focus-visible { outline: 2px solid transparent; box-shadow: var(--focus-ring); }
.prompt { color: var(--ink-muted); user-select: none; }
footer { padding-top: 16px; padding-bottom: 24px; border-top: 1px solid var(--line); font-size: 13px; line-height: 20px; color: var(--ink-muted); }
footer a { font-family: var(--font-mono); color: var(--link); border-radius: 4px; }
@keyframes glow { 50% { opacity: .45; } }
@media (prefers-reduced-motion: reduce) { .building .dot { animation: none; } }
@media (max-width: 560px) {
  main { padding-top: 32px; padding-bottom: 48px; }
  h1 { font-size: 26px; line-height: 32px; }
}
</style>
</head>
<body>
<main>
<svg class="mark" viewBox="0 0 32 32" aria-hidden="true">{{if not .Empty}}<path class="fill" d="M7.4 17.5h17.2l-1.35 8.3H8.75z"/>{{end}}<g fill="none" stroke="currentColor" stroke-width="2.75" stroke-linecap="round" stroke-linejoin="round"><path d="M8.5 12.5a7.5 7.5 0 0 1 15 0"/><path d="M4.5 12.5h23M6.3 12.5l2.1 13.4a1.6 1.6 0 0 0 1.6 1.4h12a1.6 1.6 0 0 0 1.6-1.4l2.1-13.4"/></g></svg>
<p class="state"><span class="pill {{.Tone}}"><span class="dot"></span>{{.Pill}}</span><span class="code">{{.Status}}</span></p>
<h1>{{.What}}</h1>
{{if .Fix}}<p class="fix">{{.Fix}}{{if .Command}}:{{end}}</p>
{{end}}{{if .Command}}<pre class="cmd" tabindex="0"><span class="prompt">$ </span>{{.Command}}</pre>
{{end}}</main>
<footer>This is Pail on <a href="{{.Home}}">{{.Domain}}</a>.</footer>
</body>
</html>
`
