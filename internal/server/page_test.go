package server

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/chrisdmacrae/pail/internal/storage"
)

func TestPagesForBrowsers(t *testing.T) {
	f := newFixture(t, storage.NewMemory())
	browser := []string{"Accept", "text/html,application/xhtml+xml,*/*;q=0.8"}

	rec := f.site("other.pail.lan", "/", browser...)
	wantBody(t, rec, 404, "<h1>No pail called other.</h1>")
	wantBody(t, rec, 404, `<span class="prompt">$ </span>pail up --name other</pre>`)
	wantBody(t, rec, 404, `This is Pail on <a href="http://pail.lan">pail.lan</a>.`)
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("content type: %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != pagePolicy {
		t.Errorf("policy: %q", got)
	}

	// Anything that isn't a browser gets the same words, plain.
	rec = f.site("other.pail.lan", "/")
	wantBody(t, rec, 404, "No pail called other. Make it by deploying a folder with pail up --name other.\n\nThis is Pail on pail.lan.\n")
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("content type: %q", got)
	}
	wantBody(t, f.site("example.com", "/", "Accept", "*/*"), 404, "Nothing is hosted at example.com. If it should be one of your pails, add it with pail hosts add <pail> example.com.")

	// What a request says about itself is shown as text, never as markup.
	rec = f.site("pail.lan", "/%3Cscript%3Ealert(1)%3C/script%3E.js", browser...)
	wantBody(t, rec, 404, "Nothing at /&lt;script&gt;alert(1)&lt;/script&gt;.js.")
	if strings.Contains(rec.Body.String(), "<script") {
		t.Errorf("the path reached the page as markup:\n%s", rec.Body)
	}
	wantBody(t, f.site("example.com", "/", browser...), 404, "pail hosts add &lt;pail&gt; example.com</pre>")

	if rec := f.do("HEAD", "other.pail.lan", "/", nil, browser...); rec.Code != 404 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: got %d with %d bytes", rec.Code, rec.Body.Len())
	}
}

func TestPagesTryAgainOnTheirOwn(t *testing.T) {
	f := newFixture(t, storage.NewMemory())
	starting := page{status: http.StatusServiceUnavailable, what: "api in notes is starting.", fix: "Try again in a moment.", pill: "Starting", tone: toneBuilding, retry: 2}
	show := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		f.srv.problem(rec, req, starting)
		return rec
	}

	rec := show("GET")
	wantBody(t, rec, 503, `<meta http-equiv="refresh" content="2">`)
	wantBody(t, rec, 503, `<span class="pill building"><span class="dot"></span>Starting</span>`)
	wantBody(t, rec, 503, "This page tries again on its own.")

	// Sending a form again isn't the page's to decide.
	rec = show("POST")
	wantBody(t, rec, 503, "Try again in a moment.")
	if strings.Contains(rec.Body.String(), "http-equiv") {
		t.Errorf("a POST's page reloads itself:\n%s", rec.Body)
	}
}

func TestPageFonts(t *testing.T) {
	f := newFixture(t, storage.NewMemory())
	body := f.site("other.pail.lan", "/", "Accept", "text/html").Body.String()
	fonts := regexp.MustCompile(`url\("([^"]+\.woff2)"\)`).FindAllStringSubmatch(body, -1)
	if len(fonts) != 3 {
		t.Fatalf("the page names %d fonts, want 3:\n%s", len(fonts), body)
	}
	// Every host serves them: the page can turn up on any.
	for _, font := range fonts {
		for _, host := range []string{"other.pail.lan", "example.com", "pail.lan"} {
			rec := f.site(host, font[1])
			if rec.Code != 200 || rec.Header().Get("Content-Type") != "font/woff2" || !strings.HasPrefix(rec.Body.String(), "wOF2") {
				t.Errorf("%s%s: got %d %s", host, font[1], rec.Code, rec.Header().Get("Content-Type"))
			}
			if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
				t.Errorf("%s%s: cache control %q", host, font[1], got)
			}
		}
	}
	wantBody(t, f.site("other.pail.lan", fontPath+"00000000/sans.woff2"), 404, "No pail called other.")
	wantBody(t, f.site("other.pail.lan", fontPath+fontRev+"/nope.woff2"), 404, "No pail called other.")
}
