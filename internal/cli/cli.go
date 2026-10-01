// Package cli is pail-cli: the pail command. It is a plain client of a Pail
// installation's REST API and keeps nothing of its own beyond ~/.pail/config.
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/pflag"
)

// Exit codes.
const (
	ExitOK            = 0
	ExitDeployFailed  = 1 // the deploy failed
	ExitUsage         = 2 // usage or config error, including an ambiguous profile
	ExitUnreachable   = 3 // the installation can't be reached
	ExitNotFound      = 4 // no such pail or deploy
	ExitTokenRejected = 5 // the token was rejected
)

// Env is everything the command touches outside itself.
type Env struct {
	Args   []string // without the program name
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	Home   string // the user's home folder
	Cwd    string
	// TTY says a person is at the terminal. Without one, pail never prompts.
	TTY bool
	// Stdin is where an answer to a question is read from.
	Stdin io.Reader
	// ReadSecret prompts for a line without echoing it.
	ReadSecret func(prompt string) (string, error)
	// OpenURL opens a URL in the person's browser.
	OpenURL func(url string) error
	Version string
}

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

type flags struct {
	profile string
	name    string
	json    bool
	quiet   bool
	yes     bool
	follow  bool
	help    bool
	version bool
}

type app struct {
	env   Env
	flags flags
}

const usage = `pail puts things you host at home on a Pail installation.

  pail login <url>               Add or update a profile for an installation
  pail profiles                  List profiles
  pail profiles use <name>       Set the default profile
  pail profiles rm <name>        Remove a profile
  pail up [dir] [--name <pail>]  Deploy a folder and print its URL
  pail ls                        Every pail: name, status, URL, last deploy
  pail logs <pail> [deploy]      A deploy's log; the latest by default
        --follow, -f             Keep reading until the deploy finishes
  pail deploys <pail>            The kept deploys, marking the one being served
  pail rollback <pail> <deploy>  Serve an older deploy
  pail redeploy <pail>           Deploy the latest good deploy's files again
  pail stop <pail>               Turn a pail off; it keeps its deploys
  pail start <pail>              Turn it back on
  pail hosts <pail>              A pail's hostnames, and whether each points here
  pail hosts add <pail> <host>   Add a custom hostname
  pail hosts rm <pail> <host>    Remove one
  pail open <pail>               Open the pail's URL in a browser
  pail rm <pail>                 Remove a pail and all its deploys; asks first

Flags for every command:
  -p, --profile <name>   Pick the installation
      --json             Print machine-readable output
  -q, --quiet            Print only the URL or ID
  -y, --yes              Skip confirmations

In CI, skip the config file:
  PAIL_URL=https://pail.lan PAIL_TOKEN=$PAIL_TOKEN pail up ./dist --name blog
`

// Run runs one pail command and returns its exit code.
func Run(env Env) int {
	a := &app{env: env}
	fs := pflag.NewFlagSet("pail", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVarP(&a.flags.profile, "profile", "p", "", "")
	fs.StringVar(&a.flags.name, "name", "", "")
	fs.BoolVar(&a.flags.json, "json", false, "")
	fs.BoolVarP(&a.flags.quiet, "quiet", "q", false, "")
	fs.BoolVarP(&a.flags.yes, "yes", "y", false, "")
	fs.BoolVarP(&a.flags.follow, "follow", "f", false, "")
	fs.BoolVarP(&a.flags.help, "help", "h", false, "")
	fs.BoolVar(&a.flags.version, "version", false, "")

	err := fs.Parse(env.Args)
	if errors.Is(err, pflag.ErrHelp) {
		a.flags.help = true
	} else if err != nil {
		err = usagef("%s. Run pail help.", upperFirst(err.Error()))
	} else {
		err = a.dispatch(fs.Args())
	}
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		ee = &exitError{code: ExitDeployFailed, msg: err.Error()}
	}
	if ee.msg != "" {
		fmt.Fprintln(env.Stderr, "pail: "+ee.msg)
	}
	return ee.code
}

func (a *app) dispatch(args []string) error {
	switch {
	case a.flags.version:
		fmt.Fprintln(a.env.Stdout, "pail "+a.env.Version)
		return nil
	case a.flags.help || len(args) == 0 || args[0] == "help":
		fmt.Fprint(a.env.Stdout, usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	if a.flags.name != "" && cmd != "up" {
		return usagef("--name only goes with pail up.")
	}
	switch cmd {
	case "login":
		return a.login(args)
	case "profiles":
		return a.profiles(args)
	case "up":
		return a.up(args)
	case "ls":
		return a.ls(args)
	case "logs":
		return a.logs(args)
	case "deploys":
		return a.deploys(args)
	case "rollback":
		return a.rollback(args)
	case "redeploy":
		return a.redeploy(args)
	case "stop", "start":
		return a.stopStart(cmd, args)
	case "hosts":
		return a.hosts(args)
	case "open":
		return a.open(args)
	case "rm":
		return a.rm(args)
	}
	return usagef("No command called %s. Run pail help.", cmd)
}

// interactive reports whether pail may ask a question.
func (a *app) interactive() bool {
	ci := a.env.Getenv("CI")
	return a.env.TTY && ci != "true" && ci != "1"
}

// connect picks the installation and, on a terminal, says which one first,
// so a second installation is never hit by accident.
func (a *app) connect() (*client, error) {
	t, err := a.resolve()
	if err != nil {
		return nil, err
	}
	if a.env.TTY {
		fmt.Fprintf(a.env.Stderr, "→ %s · %s\n", t.name, t.URL)
	}
	return a.newClient(t)
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.env.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// login adds or updates a profile: it checks the installation answers with
// this token, then stores the URL and the token.
func (a *app) login(args []string) error {
	if len(args) != 1 {
		return usagef("pail login takes the installation's URL: pail login https://pail.lan.")
	}
	base := normalizeURL(args[0])
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return usagef("%s isn't a URL. Try pail login https://pail.lan.", args[0])
	}

	token := strings.TrimSpace(a.env.Getenv("PAIL_TOKEN"))
	if token == "" {
		if !a.interactive() || a.env.ReadSecret == nil {
			return usagef("pail login needs the installation's token. Set PAIL_TOKEN.")
		}
		if token, err = a.env.ReadSecret("Token for " + u.Host + ": "); err != nil {
			return usagef("Couldn't read the token: %v.", err)
		}
		if token = strings.TrimSpace(token); token == "" {
			return usagef("No token given. It's the PAIL_TOKEN the server was started with.")
		}
	}

	name := a.flags.profile
	if name == "" {
		name = strings.NewReplacer(".", "-", ":", "-").Replace(u.Hostname())
	}
	c, err := a.newClient(target{name: name, profile: profile{URL: base, Token: token}})
	if err != nil {
		return err
	}
	var info apiInfo
	if err := c.get("/api/v1/info", &info); err != nil {
		return err
	}
	if info.BaseDomain == "" {
		return &exitError{code: ExitUnreachable, msg: fmt.Sprintf("%s answered, but not as Pail's API. Check the URL.", base)}
	}

	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	saved := cfg.Profiles[name]
	saved.URL, saved.Token = base, token
	cfg.Profiles[name] = saved
	if cfg.Default == "" && len(cfg.Profiles) == 1 {
		cfg.Default = name
	}
	if err := a.saveConfig(cfg); err != nil {
		return err
	}

	switch {
	case a.flags.json:
		return a.printJSON(map[string]any{"profile": name, "url": base, "base_domain": info.BaseDomain, "default": cfg.Default == name})
	case a.flags.quiet:
		fmt.Fprintln(a.env.Stdout, name)
	case cfg.Default == name:
		fmt.Fprintf(a.env.Stdout, "Saved %s as %s. It's your default.\n", u.Host, name)
	default:
		fmt.Fprintf(a.env.Stdout, "Saved %s as %s. Use it with --profile %s, or make it the default with pail profiles use %s.\n", u.Host, name, name, name)
	}
	return nil
}

func (a *app) profiles(args []string) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	path := a.shortPath(a.configPath())

	if len(args) == 0 {
		names := cfg.names()
		if a.flags.json {
			list := []map[string]any{}
			for _, n := range names {
				list = append(list, map[string]any{"name": n, "url": cfg.Profiles[n].URL, "default": n == cfg.Default})
			}
			return a.printJSON(map[string]any{"profiles": list})
		}
		if len(names) == 0 {
			fmt.Fprintln(a.env.Stdout, "No profiles yet. Run pail login <url>.")
			return nil
		}
		w := tabwriter.NewWriter(a.env.Stdout, 0, 0, 2, ' ', 0)
		for _, n := range names {
			mark := " "
			if n == cfg.Default {
				mark = "*"
			}
			if a.flags.quiet {
				fmt.Fprintln(w, n)
			} else {
				fmt.Fprintf(w, "%s %s\t%s\n", mark, n, cfg.Profiles[n].URL)
			}
		}
		return w.Flush()
	}

	if len(args) != 2 || (args[0] != "use" && args[0] != "rm") {
		return usagef("Try pail profiles, pail profiles use <name> or pail profiles rm <name>.")
	}
	name := args[1]
	if _, ok := cfg.Profiles[name]; !ok {
		return usagef("No profile called %s in %s. You have: %s.", name, path, strings.Join(cfg.names(), ", "))
	}
	if args[0] == "use" {
		cfg.Default = name
		if err := a.saveConfig(cfg); err != nil {
			return err
		}
		if !a.flags.quiet {
			fmt.Fprintf(a.env.Stdout, "%s is your default.\n", name)
		}
		return nil
	}
	delete(cfg.Profiles, name)
	if cfg.Default == name {
		cfg.Default = ""
	}
	if err := a.saveConfig(cfg); err != nil {
		return err
	}
	if !a.flags.quiet {
		fmt.Fprintf(a.env.Stdout, "Removed profile %s. The installation itself isn't touched.\n", name)
	}
	return nil
}

func (a *app) ls(args []string) error {
	if len(args) != 0 {
		return usagef("pail ls takes no arguments.")
	}
	c, err := a.connect()
	if err != nil {
		return err
	}
	var raw struct {
		Pails []json.RawMessage `json:"pails"`
	}
	if err := c.get("/api/v1/pails", &raw); err != nil {
		return err
	}
	if a.flags.json {
		return a.printJSON(raw)
	}
	if len(raw.Pails) == 0 && !a.flags.quiet {
		fmt.Fprintln(a.env.Stdout, "Nothing in the pail yet. Run pail up from the folder you build.")
		return nil
	}

	w := tabwriter.NewWriter(a.env.Stdout, 0, 0, 2, ' ', 0)
	if !a.flags.quiet {
		fmt.Fprintln(w, "NAME\tSTATUS\tURL\tLAST DEPLOY")
	}
	for _, r := range raw.Pails {
		var p apiPail
		if err := json.Unmarshal(r, &p); err != nil {
			return err
		}
		if a.flags.quiet {
			fmt.Fprintln(w, p.URL)
			continue
		}
		last := "—"
		if p.Deploy != nil {
			last = p.Deploy.ID + " · " + ago(time.Since(p.Deploy.CreatedAt))
			// After a rollback or a failure, that isn't what's being served.
			if p.Serving != "" && p.Serving != p.Deploy.ID {
				last += " (serving " + p.Serving + ")"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, upperFirst(p.Status), p.URL, last)
	}
	return w.Flush()
}

func (a *app) logs(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usagef("pail logs takes a pail, and a deploy if you don't want the latest: pail logs blog.")
	}
	c, err := a.connect()
	if err != nil {
		return err
	}
	name := args[0]
	var deploy string
	if len(args) == 2 {
		deploy = args[1]
	} else {
		var p apiPail
		if err := c.get("/api/v1/pails/"+name, &p); err != nil {
			return err
		}
		if p.Deploy == nil {
			return &exitError{code: ExitNotFound, msg: name + " has no deploys yet."}
		}
		deploy = p.Deploy.ID
	}
	_, err = c.streamLog(name, deploy, a.flags.follow, func(l apiLine) { a.printLine(a.env.Stdout, l) })
	return err
}

// deploys lists the deploys a pail keeps, marking the one being served.
func (a *app) deploys(args []string) error {
	if len(args) != 1 {
		return usagef("pail deploys takes a pail: pail deploys blog.")
	}
	c, err := a.connect()
	if err != nil {
		return err
	}
	var raw struct {
		Deploys []json.RawMessage `json:"deploys"`
		Serving string            `json:"serving"`
	}
	if err := c.get("/api/v1/pails/"+args[0]+"/deploys", &raw); err != nil {
		return err
	}
	if a.flags.json {
		return a.printJSON(raw)
	}

	w := tabwriter.NewWriter(a.env.Stdout, 0, 0, 2, ' ', 0)
	if !a.flags.quiet {
		fmt.Fprintln(w, "DEPLOY\tWHEN\tWHAT\t")
	}
	for _, r := range raw.Deploys {
		var d apiDeploy
		if err := json.Unmarshal(r, &d); err != nil {
			return err
		}
		if a.flags.quiet {
			fmt.Fprintln(w, d.ID)
			continue
		}
		// One word says where a deploy stands; a good one that isn't being
		// served needs none.
		mark := ""
		switch {
		case d.Serving:
			mark = "Serving"
		case d.State != "ok":
			mark = upperFirst(d.State)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.ID, ago(time.Since(d.CreatedAt)), d.Label, mark)
	}
	return w.Flush()
}

// rollback serves an older deploy. Nothing is rebuilt: the pail's pointer
// moves to a deploy it already has.
func (a *app) rollback(args []string) error {
	if len(args) != 2 {
		return usagef("pail rollback takes a pail and a deploy: pail rollback blog 9b1e07d. pail deploys blog lists them.")
	}
	c, err := a.connect()
	if err != nil {
		return err
	}
	name, deploy := args[0], args[1]
	var raw json.RawMessage
	if err := c.post("/api/v1/pails/"+name+"/serve", map[string]string{"deploy": deploy}, &raw); err != nil {
		return err
	}
	var p apiPail
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	switch {
	case a.flags.json:
		return a.printJSON(raw)
	case a.flags.quiet:
		fmt.Fprintln(a.env.Stdout, p.URL)
	default:
		fmt.Fprintf(a.env.Stdout, "%s is serving %s.\n%s\n", p.Name, p.Serving, p.URL)
	}
	return nil
}

// onePail checks a command was given exactly one pail and connects.
func (a *app) onePail(cmd string, args []string) (*client, string, error) {
	if len(args) != 1 {
		return nil, "", usagef("pail %s takes a pail: pail %s blog.", cmd, cmd)
	}
	c, err := a.connect()
	return c, args[0], err
}

// redeploy deploys the pail's latest good files again and follows the log.
func (a *app) redeploy(args []string) error {
	c, name, err := a.onePail("redeploy", args)
	if err != nil {
		return err
	}
	var started apiDeploy
	if err := c.post("/api/v1/pails/"+name+"/redeploy", nil, &started); err != nil {
		return err
	}
	return a.follow(c, name, started)
}

// stopStart turns a pail off or back on.
func (a *app) stopStart(cmd string, args []string) error {
	c, name, err := a.onePail(cmd, args)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.post("/api/v1/pails/"+name+"/"+cmd, nil, &raw); err != nil {
		return err
	}
	var p apiPail
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	switch {
	case a.flags.json:
		return a.printJSON(raw)
	case a.flags.quiet:
		fmt.Fprintln(a.env.Stdout, p.URL)
	case cmd == "stop":
		fmt.Fprintf(a.env.Stdout, "%s is off. %s answers nothing until you run pail start %s.\n", p.Name, p.URL, p.Name)
	default:
		fmt.Fprintf(a.env.Stdout, "%s is back on.\n%s\n", p.Name, p.URL)
	}
	return nil
}

// pointing says in a word or three whether a hostname's traffic gets here.
func pointing(h apiHost) string {
	if h.PointsHere {
		return "Points here"
	}
	return "Not pointing here yet"
}

// hosts lists, adds or removes a pail's custom hostnames, and reports
// whether each one resolves to the installation yet.
func (a *app) hosts(args []string) error {
	const usage = "Try pail hosts blog, pail hosts add blog blog.home.example or pail hosts rm blog blog.home.example."
	action := "list"
	if len(args) == 3 && (args[0] == "add" || args[0] == "rm") {
		action, args = args[0], args[1:]
	} else if len(args) != 1 {
		return usagef(usage)
	}
	c, err := a.connect()
	if err != nil {
		return err
	}
	path := "/api/v1/pails/" + args[0] + "/hosts"

	switch action {
	case "add":
		var raw json.RawMessage
		if err := c.post(path, map[string]string{"host": args[1]}, &raw); err != nil {
			return err
		}
		var h apiHost
		if err := json.Unmarshal(raw, &h); err != nil {
			return err
		}
		switch {
		case a.flags.json:
			return a.printJSON(raw)
		case a.flags.quiet:
			fmt.Fprintln(a.env.Stdout, h.URL)
		case h.PointsHere:
			fmt.Fprintf(a.env.Stdout, "Added %s to %s. It points here.\n%s\n", h.Host, args[0], h.URL)
		default:
			fmt.Fprintf(a.env.Stdout, "Added %s to %s. It isn't pointing here yet: give it a CNAME to this Pail's base domain, or an A record to this server's address.\n", h.Host, args[0])
		}
		return nil

	case "rm":
		resp, err := c.do("DELETE", path+"/"+url.PathEscape(args[1]), nil, 0, "")
		if err != nil {
			return err
		}
		resp.Body.Close()
		if !a.flags.quiet && !a.flags.json {
			fmt.Fprintf(a.env.Stdout, "Removed %s from %s.\n", args[1], args[0])
		}
		return nil
	}

	var raw struct {
		Hosts []json.RawMessage `json:"hosts"`
	}
	if err := c.get(path, &raw); err != nil {
		return err
	}
	if a.flags.json {
		return a.printJSON(raw)
	}
	w := tabwriter.NewWriter(a.env.Stdout, 0, 0, 2, ' ', 0)
	for _, r := range raw.Hosts {
		var h apiHost
		if err := json.Unmarshal(r, &h); err != nil {
			return err
		}
		if a.flags.quiet {
			fmt.Fprintln(w, h.URL)
			continue
		}
		kind := ""
		if h.Default {
			kind = "Default"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", h.Host, kind, pointing(h))
	}
	return w.Flush()
}

// open opens the pail's URL in a browser, and prints it either way.
func (a *app) open(args []string) error {
	c, name, err := a.onePail("open", args)
	if err != nil {
		return err
	}
	var p apiPail
	if err := c.get("/api/v1/pails/"+name, &p); err != nil {
		return err
	}
	fmt.Fprintln(a.env.Stdout, p.URL)
	if a.env.OpenURL == nil {
		return nil
	}
	if err := a.env.OpenURL(p.URL); err != nil {
		fmt.Fprintf(a.env.Stderr, "pail: Couldn't open a browser (%v). The URL is above.\n", err)
	}
	return nil
}

// rm removes a pail and all its deploys. It asks first unless --yes.
func (a *app) rm(args []string) error {
	c, name, err := a.onePail("rm", args)
	if err != nil {
		return err
	}
	var p apiPail
	if err := c.get("/api/v1/pails/"+name, &p); err != nil {
		return err
	}
	if !a.flags.yes {
		if !a.interactive() {
			return usagef("pail rm asks before removing, and there's no terminal to ask on. Add --yes.")
		}
		fmt.Fprintf(a.env.Stderr, "Remove %s? %s stops answering, and every deploy is deleted. This can't be undone. [y/N] ", p.Name, p.URL)
		answer, _ := bufio.NewReader(a.env.Stdin).ReadString('\n')
		if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
			fmt.Fprintf(a.env.Stdout, "Kept %s.\n", p.Name)
			return nil
		}
	}
	resp, err := c.do("DELETE", "/api/v1/pails/"+name, nil, 0, "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch {
	case a.flags.json:
		return a.printJSON(map[string]string{"removed": p.Name})
	case a.flags.quiet:
		fmt.Fprintln(a.env.Stdout, p.Name)
	default:
		fmt.Fprintf(a.env.Stdout, "Removed %s.\n", p.Name)
	}
	return nil
}

// printLine prints one log line: as JSON with --json, else with its time.
func (a *app) printLine(w io.Writer, l apiLine) {
	if a.flags.json {
		b, _ := json.Marshal(l)
		fmt.Fprintln(w, string(b))
		return
	}
	fmt.Fprintf(w, "%s  %s\n", l.Time.Local().Format("15:04:05"), l.Text)
}

// ago says how long ago something happened, the way the pail list does.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
	return fmt.Sprintf("%d weeks ago", int(d.Hours()/(24*7)))
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
