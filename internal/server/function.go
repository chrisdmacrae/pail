package server

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/chrisdmacrae/pail/internal/pails"
)

// maxUnsizedBody is the most Pail holds of a request body that didn't say
// how long it is, to tell the function its length.
const maxUnsizedBody = 8 << 20

// serveFunction answers a request with one of the live deploy's functions.
// The interface is CGI: the request goes in as environment variables and
// standard input, and what the program writes to standard output, headers
// first, is the response.
func (s *Server) serveFunction(w http.ResponseWriter, r *http.Request, live pails.Live, name string) {
	body, length := io.Reader(r.Body), r.ContentLength
	switch {
	case length > s.cfg.MaxUploadSize && s.cfg.MaxUploadSize > 0:
		s.plainPage(w, r, http.StatusRequestEntityTooLarge, "That's more than "+name+" can be sent.")
		return
	case length < 0:
		// CGI tells the program how much is coming, so it all has to be here.
		held, err := io.ReadAll(io.LimitReader(r.Body, maxUnsizedBody+1))
		if err != nil || len(held) > maxUnsizedBody {
			s.plainPage(w, r, http.StatusRequestEntityTooLarge, "That's more than "+name+" can be sent without saying how long it is.")
			return
		}
		body, length = bytes.NewReader(held), int64(len(held))
	}

	out, err := s.pails.Invoke(r.Context(), live, name, pails.FunctionRequest{Env: cgiEnv(r, length), Body: body, BodyLen: length})
	var failure pails.FunctionFailure
	switch {
	case r.Context().Err() != nil:
		return // the visitor left
	case errors.Is(err, pails.ErrFunctionBusy):
		w.Header().Set("Retry-After", "1")
		s.plainPage(w, r, http.StatusServiceUnavailable, name+" in "+live.Pail+" is busy. Try again in a moment.")
		return
	case errors.Is(err, pails.ErrFunctionDown):
		s.plainPage(w, r, http.StatusServiceUnavailable, name+" in "+live.Pail+" isn't running. pail logs "+live.Pail+" --output says why.")
		return
	case errors.As(err, &failure):
		s.plainPage(w, r, http.StatusInternalServerError, failure.Why+" See what it printed with pail logs "+live.Pail+" --output.")
		return
	case err != nil:
		s.log.Error("function", "pail", live.Pail, "function", name, "err", err)
		s.plainPage(w, r, http.StatusInternalServerError, name+" in "+live.Pail+" couldn't run.")
		return
	}

	status, header, content, err := parseCGI(out)
	if err != nil {
		s.pails.LogFunction(live, name, "%s answered, but not as CGI: %v. Write header lines, then a blank line, then the body.", name, err)
		s.plainPage(w, r, http.StatusInternalServerError, name+" in "+live.Pail+" answered in a way Pail couldn't read. See pail logs "+live.Pail+" --output.")
		return
	}
	h := w.Header()
	for key, values := range header {
		h[key] = values
	}
	h.Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(content)
	}
}

// cgiEnv is a request as CGI's environment variables (RFC 3875).
func cgiEnv(r *http.Request, length int64) []string {
	host, port, _ := net.SplitHostPort(r.Host)
	if host == "" {
		host = r.Host
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
		if port == "" {
			port = "443"
		}
	} else if port == "" {
		port = "80"
	}
	remote, _, _ := net.SplitHostPort(r.RemoteAddr)
	env := []string{
		"GATEWAY_INTERFACE=CGI/1.1",
		"SERVER_SOFTWARE=pail",
		"SERVER_PROTOCOL=" + r.Proto,
		"SERVER_NAME=" + host,
		"SERVER_PORT=" + port,
		"REQUEST_SCHEME=" + scheme,
		"REQUEST_METHOD=" + r.Method,
		// The full request path: a function is given no script name to take
		// off the front of it.
		"SCRIPT_NAME=",
		"PATH_INFO=" + r.URL.Path,
		"QUERY_STRING=" + r.URL.RawQuery,
		"REQUEST_URI=" + r.URL.RequestURI(),
		"REMOTE_ADDR=" + remote,
		"CONTENT_LENGTH=" + strconv.FormatInt(length, 10),
		"CONTENT_TYPE=" + r.Header.Get("Content-Type"),
	}
	if scheme == "https" {
		env = append(env, "HTTPS=on")
	}
	env = append(env, "HTTP_HOST="+r.Host)
	for key, values := range r.Header {
		switch key {
		case "Content-Type", "Content-Length", "Proxy": // the first two have names of their own; the last is never trusted
			continue
		}
		name := "HTTP_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
		joiner := ", "
		if key == "Cookie" {
			joiner = "; "
		}
		env = append(env, name+"="+strings.Join(values, joiner))
	}
	return env
}

// hopHeaders are about one connection, and not a program's to set.
var hopHeaders = []string{"Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade", "Content-Length", "Status"}

// parseCGI splits a program's output into the response it describes:
// header lines, a blank line, the body. A Status header sets the status;
// without one it is 200, or 302 when there is a Location.
func parseCGI(out []byte) (status int, header http.Header, body []byte, err error) {
	r := bufio.NewReader(bytes.NewReader(out))
	mime, err := textproto.NewReader(r).ReadMIMEHeader()
	if err != nil {
		if len(bytes.TrimSpace(out)) == 0 {
			return 0, nil, nil, errors.New("it wrote nothing")
		}
		return 0, nil, nil, errors.New("its output doesn't start with header lines")
	}
	header = http.Header(mime)
	status = http.StatusOK
	if line := header.Get("Status"); line != "" {
		code, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if status, err = strconv.Atoi(code); err != nil || status < 200 || status > 599 {
			return 0, nil, nil, errors.New("its Status header isn't a status, like 404")
		}
	} else if header.Get("Location") != "" {
		status = http.StatusFound
	}
	for _, key := range hopHeaders {
		header.Del(key)
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	body, _ = io.ReadAll(r)
	return status, header, body, nil
}
