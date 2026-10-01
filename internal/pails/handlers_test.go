package pails

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A function in a language with a handler program can be a handler, called
// with the request and a response to fill in, or a program that writes CGI.
// These run each language's handler program for real, where this machine
// has the language.
func TestHandlers(t *testing.T) {
	type source map[string]string
	for _, lang := range []struct {
		name, main string
		// echo answers with what it was asked, returned says its answer by
		// returning it, moved redirects, broken fails, and cgi is a program
		// that answers for itself.
		echo, returned, moved, broken, cgi string
	}{
		{
			name: "node", main: "index.js",
			echo: `console.log("loading");
export default async function (req, res) {
  console.log("handling");
  res.status(201);
  res.contentType("application/json");
  return res.send(JSON.stringify({ method: req.method, path: req.path, url: req.url, query: req.query, agent: req.headers["user-agent"], type: req.headers["content-type"], body: req.json(), text: req.text() }));
}`,
			returned: `module.exports = (req) => ({ form: req.form() });`,
			moved: `export default (req, res) => {
  res.append("Set-Cookie", "a=1").append("Set-Cookie", "b=2").set("X-Kept", "no").set("x-kept", "yes").redirect("/elsewhere");
  setInterval(() => {}, 1000); // left open, and not waited for
};`,
			broken: `export default (req, res) => { res.send("never seen"); throw new Error("boom"); };`,
			cgi: `console.log("Status: 404\nContent-Type: text/plain\n");
process.stdin.on("data", (body) => process.stdout.write("no " + body));`,
		},
		{
			name: "python", main: "main.py",
			echo: `import helper
print("loading")
def handler(req, res):
    print("handling")
    res.status(201)
    res.content_type("application/json")
    return res.send({"method": req.method, "path": req.path, "url": req.url, "query": req.query, "agent": req.headers["user-agent"], "type": req.headers["content-type"], "body": req.json(), "text": req.text()})`,
			returned: `async def handler(req, res):
    return {"form": req.form()}`,
			moved: `def handler(req, res):
    res.append("Set-Cookie", "a=1").append("Set-Cookie", "b=2").set("X-Kept", "no").set("x-kept", "yes").redirect("/elsewhere")`,
			broken: `def handler(req, res):
    res.send("never seen")
    raise RuntimeError("boom")`,
			cgi: `import os, sys
body = sys.stdin.read()
print("Status: 404\nContent-Type: text/plain\n")
sys.stdout.flush()
os.write(1, b"no " + body.encode())
sys.exit(0)`,
		},
		{
			name: "ruby", main: "main.rb",
			echo: `puts "loading"
def handler(req, res)
  STDOUT.puts "handling"
  res.status(201)
  res.content_type("application/json")
  res.send({method: req.method, path: req.path, url: req.url, query: req.query, agent: req.headers["user-agent"], type: req.headers["content-type"], body: req.json, text: req.text})
end`,
			returned: `def handler(req, res)
  {form: req.form}
end`,
			moved: `def handler(req, res)
  res.append("Set-Cookie", "a=1").append("Set-Cookie", "b=2").set("X-Kept", "no").set("x-kept", "yes").redirect("/elsewhere")
end`,
			broken: `def handler(req, res)
  res.send("never seen")
  raise "boom"
end`,
			cgi: `body = STDIN.read
puts "Status: 404\nContent-Type: text/plain\n\n"
STDOUT.write("no " + body)
exit 0`,
		},
	} {
		t.Run(lang.name, func(t *testing.T) {
			l := languageNamed(lang.name)
			// run answers one request with a function whose file holds
			// program, and returns what it wrote and how it ended.
			run := func(program, body string, env ...string) (head, content, stderr string, exit int) {
				t.Helper()
				dir := t.TempDir()
				fill := func(dir string) error {
					os.WriteFile(filepath.Join(dir, "helper.py"), nil, 0o644)
					return os.WriteFile(filepath.Join(dir, lang.main), []byte(program), 0o644)
				}
				plan, err := planFunction("api", functionConfig{}, map[string]bool{lang.main: true})
				if err != nil || plan.lang != l || plan.handler == "" {
					t.Fatalf("plan: %+v, %v", plan, err)
				}
				if err := withHandler(fill, plan.handler)(dir); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(plan.argv[0], plan.argv[1:]...)
				cmd.Dir = dir
				cmd.Env = append(append([]string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + t.TempDir()}, l.env...), env...)
				cmd.Stdin = strings.NewReader(body)
				var out, said bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &said
				if err := cmd.Run(); err != nil {
					exit = cmd.ProcessState.ExitCode()
				}
				head, content, _ = strings.Cut(strings.ReplaceAll(out.String(), "\r\n", "\n"), "\n\n")
				return head, content, said.String(), exit
			}
			if _, err := exec.LookPath(l.run(nil, lang.main)[0]); err != nil {
				t.Skipf("this machine has no %s", lang.name)
			}

			// A handler is given the request, and what it sets is the response.
			head, content, said, exit := run(lang.echo, `{"n": 1}`, "REQUEST_METHOD=POST", "PATH_INFO=/api/notes/7", "QUERY_STRING=page=2&q=two+words",
				"REQUEST_URI=/api/notes/7?page=2&q=two+words", "CONTENT_TYPE=application/json", "CONTENT_LENGTH=8", "HTTP_USER_AGENT=tests")
			if exit != 0 || head != "Status: 201\nContent-Type: application/json" {
				t.Fatalf("the answer: exit %d, %q\n%s", exit, head, said)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(content), &got); err != nil {
				t.Fatalf("the body isn't what the handler sent: %v\n%s", err, content)
			}
			want := map[string]any{"method": "POST", "path": "/api/notes/7", "url": "/api/notes/7?page=2&q=two+words", "agent": "tests", "type": "application/json", "text": `{"n": 1}`}
			for key, want := range want {
				if got[key] != want {
					t.Errorf("%s: got %v, want %v", key, got[key], want)
				}
			}
			if query, _ := got["query"].(map[string]any); query["page"] != "2" || query["q"] != "two words" {
				t.Errorf("query: %v", got["query"])
			}
			if body, _ := got["body"].(map[string]any); body["n"] != float64(1) {
				t.Errorf("body: %v", got["body"])
			}
			// What it prints is for the pail's output, not the response.
			if !strings.Contains(said, "loading") || !strings.Contains(said, "handling") {
				t.Errorf("what the handler printed: %q", said)
			}

			// What a handler returns is sent, if it sent nothing itself.
			head, content, said, exit = run(lang.returned, "name=Ada+L&note=caf%C3%A9", "REQUEST_METHOD=POST", "CONTENT_LENGTH=25")
			if json.Unmarshal([]byte(content), &got); exit != 0 || head != "Status: 200\nContent-Type: application/json" {
				t.Fatalf("a returned answer: exit %d, %q\n%s", exit, head, said)
			}
			if form, _ := got["form"].(map[string]any); form["name"] != "Ada L" || form["note"] != "café" {
				t.Errorf("form: %s", content)
			}

			head, content, said, exit = run(lang.moved, "")
			if exit != 0 || head != "Status: 302\nSet-Cookie: a=1\nSet-Cookie: b=2\nx-kept: yes\nLocation: /elsewhere" || content != "" {
				t.Errorf("a redirect: exit %d, %q %q\n%s", exit, head, content, said)
			}

			// A handler that fails answers nothing: Pail says what happened.
			head, content, said, exit = run(lang.broken, "")
			if exit == 0 || head+content != "" || !strings.Contains(said, "boom") {
				t.Errorf("a handler that fails: exit %d, %q %q\n%s", exit, head, content, said)
			}

			// A file with no handler is a program, and what it writes is the
			// response, as it would be without the handler program.
			head, content, said, exit = run(lang.cgi, "such recipe", "REQUEST_METHOD=POST", "CONTENT_LENGTH=11")
			if exit != 0 || head != "Status: 404\nContent-Type: text/plain" || strings.TrimSpace(content) != "no such recipe" {
				t.Errorf("a program that writes CGI: exit %d, %q %q\n%s", exit, head, content, said)
			}
		})
	}
}

// A function with a cmd of its own is run as pail.json says, with no
// handler program; so is one in a language that has none.
func TestHandlerPlans(t *testing.T) {
	for _, c := range []struct {
		files   []string
		fc      functionConfig
		argv    string
		handler string
	}{
		{files: []string{"index.js"}, argv: "node .pail-handler.mjs index.js", handler: "handler.mjs"},
		{files: []string{"package.json", "server.js"}, argv: `sh -c exec node .pail-handler.mjs "$(cat .pail-main)"`, handler: "handler.mjs"},
		{files: []string{"Gemfile", "main.rb"}, argv: "bundle exec ruby .pail-handler.rb main.rb", handler: "handler.rb"},
		{files: []string{"main.py"}, fc: functionConfig{cmd: []string{"python3", "app.py"}}, argv: "python3 app.py"},
		{files: []string{"go.mod", "main.go"}, argv: "./fn"},
		{files: []string{"main.sh"}, argv: "sh main.sh"},
	} {
		names := map[string]bool{}
		for _, f := range c.files {
			names[f] = true
		}
		plan, err := planFunction("api", c.fc, names)
		if err != nil || strings.Join(plan.argv, " ") != c.argv || plan.handler != c.handler {
			t.Errorf("%v: got %q with handler %q (%v), want %q with %q", c.files, plan.argv, plan.handler, err, c.argv, c.handler)
		}
	}
}
