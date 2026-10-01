package pails

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chrisdmacrae/pail/internal/storage"
)

// A pail has variables of its own: settings and secrets kept on the server,
// for pail.json to use in a container's or a function's env as ${NAME}. They
// are what a repo shouldn't hold: a database password, an address that is
// one installation's own.
//
// A secret is sealed before it is stored, with a key the object store never
// sees, and is never handed back by the API. pail.json's ${NAME} is filled in
// when a container or a function starts, so what a deploy keeps is the
// reference and not the value.

var (
	ErrBadVariable  = errors.New("not a variable name")
	ErrNoVariable   = errors.New("pail has no such variable")
	ErrValueTooLong = errors.New("variable value is too long")
	// ErrNoSecretKey says this Pail has no key to seal secrets with.
	ErrNoSecretKey = errors.New("no key for secrets")
	// ErrSealed says a secret was sealed with another key than this Pail's.
	ErrSealed = errors.New("secret was sealed with another key")
)

// MaxVariableValue is the longest value a variable may have, in bytes.
const MaxVariableValue = 64 << 10

// Variable is one of a pail's variables, as the API shows it. A secret's
// value is never shown.
type Variable struct {
	Name      string    `json:"name"`
	Value     string    `json:"value,omitempty"`
	Secret    bool      `json:"secret"`
	UpdatedAt time.Time `json:"updated_at"`
}

// storedVariable is a variable as the object store holds it: its value, or
// for a secret, the value sealed.
type storedVariable struct {
	Name      string    `json:"name"`
	Value     string    `json:"value,omitempty"`
	Sealed    string    `json:"sealed,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type storedEnv struct {
	Variables []storedVariable `json:"variables"`
}

func envKey(name string) string { return metaPrefix(name) + "env.json" }

// seal encrypts a secret's value for one variable of one pail. Which pail
// and which variable are bound into it, so a sealed value moved to another
// doesn't open.
func (s *Service) seal(pail, name, value string) (string, error) {
	aead, err := s.aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(value), []byte(pail+"\x00"+name))), nil
}

func (s *Service) unseal(pail, name, sealed string) (string, error) {
	aead, err := s.aead()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < aead.NonceSize() {
		return "", ErrSealed
	}
	value, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(pail+"\x00"+name))
	if err != nil {
		return "", ErrSealed
	}
	return string(value), nil
}

func (s *Service) aead() (cipher.AEAD, error) {
	if len(s.secretKey) != 32 {
		return nil, ErrNoSecretKey
	}
	block, err := aes.NewCipher(s.secretKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// CanKeepSecrets says whether this Pail has a key to seal secrets with.
func (s *Service) CanKeepSecrets() bool { return len(s.secretKey) == 32 }

func (s *Service) readEnv(ctx context.Context, name string) (storedEnv, error) {
	var env storedEnv
	err := s.readJSON(ctx, envKey(name), &env)
	if errors.Is(err, storage.ErrNotFound) {
		return env, nil
	}
	return env, err
}

// Variables lists a pail's variables by name. A secret comes without its
// value. The pail needn't exist yet: its variables can be set before its
// first deploy, which may need them.
func (s *Service) Variables(ctx context.Context, name string) ([]Variable, error) {
	if !ValidName(name) {
		return nil, ErrBadName
	}
	env, err := s.readEnv(ctx, name)
	if err != nil {
		return nil, err
	}
	vars := make([]Variable, 0, len(env.Variables))
	for _, v := range env.Variables {
		vars = append(vars, v.view())
	}
	return vars, nil
}

func (v storedVariable) view() Variable {
	if v.Sealed != "" {
		return Variable{Name: v.Name, Secret: true, UpdatedAt: v.UpdatedAt}
	}
	return Variable{Name: v.Name, Value: v.Value, UpdatedAt: v.UpdatedAt}
}

// SetVariable sets one of a pail's variables, replacing any by that name. A
// secret is sealed before it is stored. Containers that are running keep
// what they started with: the next deploy uses the new value.
func (s *Service) SetVariable(ctx context.Context, name, key, value string, secret bool) (Variable, error) {
	switch {
	case !ValidName(name):
		return Variable{}, ErrBadName
	case !envNameRE.MatchString(key):
		return Variable{}, ErrBadVariable
	case len(value) > MaxVariableValue:
		return Variable{}, ErrValueTooLong
	}
	v := storedVariable{Name: key, Value: value, UpdatedAt: time.Now().UTC()}
	if secret {
		sealed, err := s.seal(name, key, value)
		if err != nil {
			return Variable{}, err
		}
		v.Value, v.Sealed = "", sealed
	}

	s.envMu.Lock()
	defer s.envMu.Unlock()
	if s.busyRemoving(name) {
		return Variable{}, ErrBusy
	}
	env, err := s.readEnv(ctx, name)
	if err != nil {
		return Variable{}, err
	}
	at := sort.Search(len(env.Variables), func(i int) bool { return env.Variables[i].Name >= key })
	if at < len(env.Variables) && env.Variables[at].Name == key {
		env.Variables[at] = v
	} else {
		env.Variables = append(env.Variables[:at], append([]storedVariable{v}, env.Variables[at:]...)...)
	}
	if err := s.writeJSON(ctx, envKey(name), env); err != nil {
		return Variable{}, err
	}
	return v.view(), nil
}

// RemoveVariable forgets one of a pail's variables.
func (s *Service) RemoveVariable(ctx context.Context, name, key string) error {
	if !ValidName(name) {
		return ErrBadName
	}
	s.envMu.Lock()
	defer s.envMu.Unlock()
	env, err := s.readEnv(ctx, name)
	if err != nil {
		return err
	}
	for i, v := range env.Variables {
		if v.Name == key {
			env.Variables = append(env.Variables[:i], env.Variables[i+1:]...)
			return s.writeJSON(ctx, envKey(name), env)
		}
	}
	return ErrNoVariable
}

func (s *Service) busyRemoving(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.removing[name]
}

// variables returns a pail's variables with their values, secrets opened,
// for filling in pail.json.
func (s *Service) variables(ctx context.Context, name string) (map[string]string, error) {
	env, err := s.readEnv(ctx, name)
	if err != nil {
		return nil, err
	}
	vars := make(map[string]string, len(env.Variables))
	for _, v := range env.Variables {
		if v.Sealed == "" {
			vars[v.Name] = v.Value
			continue
		}
		value, err := s.unseal(name, v.Name, v.Sealed)
		switch {
		case errors.Is(err, ErrNoSecretKey):
			return nil, userErrorf("%s's secret %s can't be read: this Pail has no key for secrets. Give it back the key it had, in PAIL_SECRETS_KEY or its data folder's secrets.key.", name, v.Name)
		case err != nil:
			return nil, userErrorf("%s's secret %s can't be read: it was kept with another key than this Pail has now. Set it again, or give Pail back the key it had.", name, v.Name)
		}
		vars[v.Name] = value
	}
	return vars, nil
}

// A reference in pail.json: ${NAME}, or ${NAME:-what to use if it isn't
// set}. $${ is a way to write ${ and have it left alone.
var refRE = regexp.MustCompile(`\$\$\{|\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// expand fills in the references in one of pail.json's values. It returns
// the names it has no value for.
func expand(value string, vars map[string]string) (string, []string) {
	if !strings.Contains(value, "${") {
		return value, nil
	}
	var missing []string
	out := refRE.ReplaceAllStringFunc(value, func(ref string) string {
		if ref == "$${" {
			return "${"
		}
		m := refRE.FindStringSubmatch(ref)
		if v, ok := vars[m[1]]; ok {
			return v
		}
		if m[2] != "" {
			return m[3]
		}
		missing = append(missing, m[1])
		return ""
	})
	return out, missing
}

// expandEnv fills in the references in an env from pail.json, for the
// container or function called owner. A reference to a variable the pail
// doesn't have is an error that says how to set it.
func expandEnv(pail, owner string, env map[string]string, vars map[string]string) (map[string]string, error) {
	if len(env) == 0 {
		return env, nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(env))
	for _, k := range keys {
		v, missing := expand(env[k], vars)
		if len(missing) > 0 {
			return nil, userErrorf("pail.json uses ${%[1]s} for %[2]s's %[3]s, and %[4]s has no variable called %[1]s. Set it on the pail's page, or with: pail env set %[4]s %[1]s=…", missing[0], owner, k, pail)
		}
		out[k] = v
	}
	return out, nil
}

// checkVariables says whether a pail has every variable its pail.json uses,
// before anything is built.
func (s *Service) checkVariables(ctx context.Context, name string, cfg deployConfig) error {
	uses := false
	for _, c := range cfg.containers {
		uses = uses || usesVariables(c.Env)
	}
	for _, f := range cfg.functions {
		uses = uses || usesVariables(f.Env)
	}
	if !uses {
		return nil
	}
	vars, err := s.variables(ctx, name)
	if err != nil {
		return err
	}
	for _, c := range cfg.names() {
		if _, err := expandEnv(name, c, cfg.containers[c].Env, vars); err != nil {
			return err
		}
	}
	for _, f := range cfg.functionNames() {
		if _, err := expandEnv(name, f, cfg.functions[f].Env, vars); err != nil {
			return err
		}
	}
	return nil
}

func usesVariables(env map[string]string) bool {
	for _, v := range env {
		if strings.Contains(v, "${") {
			return true
		}
	}
	return false
}
