package tests

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin the HTTP wire contract of the running server:
// status codes, response headers, content types (including the presence or
// absence of a charset) and raw body bytes for the routing, static-file,
// serialisation, error-mapping and authentication seams.
//
// They deliberately talk to the packaged server over a real socket. An
// in-process handler test reports the response the router would have built,
// not the bytes the server actually writes, and several of the contracts
// below live entirely in those bytes.

const (
	notFoundBody     = "404 page not found\n"
	unauthorizedBody = "Authentication required\n"
	plainTextUTF8    = "text/plain; charset=utf-8"
)

type wireResponse struct {
	status int
	header http.Header
	body   []byte
}

func (w wireResponse) contentType() string { return w.header.Get("Content-Type") }

// probe issues a single request and captures the raw response. Redirects are
// surfaced rather than followed so redirect behaviour can be asserted.
func probe(t *testing.T, method, apipath string, body []byte, opts ...func(*http.Request)) wireResponse {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, getURL(apipath), reader)
	require.NoError(t, err)
	for _, opt := range opts {
		opt(req)
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return wireResponse{status: resp.StatusCode, header: resp.Header, body: raw}
}

func withToken(req *http.Request) {
	if len(Token) > 0 {
		req.AddCookie(&http.Cookie{Name: "token", Value: Token})
	}
}

func withJSON(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
}

// allowSet reads the Allow header as a sorted multiset. The values are
// compared as a multiset because the router this project migrated away from
// emitted one header line per allowed method in Go map iteration order, which
// differs between otherwise identical requests. Duplicates are significant and
// are therefore kept.
func allowSet(header http.Header) []string {
	var out []string
	for _, value := range header.Values("Allow") {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	sort.Strings(out)
	return out
}

func assertNotFoundEnvelope(t *testing.T, resp wireResponse, what string) {
	t.Helper()
	assert.Equal(t, http.StatusNotFound, resp.status, what)
	assert.Equal(t, plainTextUTF8, resp.contentType(), what)
	assert.Equal(t, "nosniff", resp.header.Get("X-Content-Type-Options"), what)
	assert.Equal(t, notFoundBody, string(resp.body), what)
}

// TestSeamRoutes checks that every route is reachable at exactly the path and
// method it was declared with, and only there. A migration that mounts the API
// under a prefix, or that drops a verb, still compiles and still leaves the
// rest of the suite green.
func TestSeamRoutes(t *testing.T) {
	declared := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "api/nextdate"},
		{http.MethodPost, "api/task"},
		{http.MethodGet, "api/tasks"},
		{http.MethodGet, "api/task"},
		{http.MethodPut, "api/task"},
		{http.MethodDelete, "api/task"},
		{http.MethodPost, "api/task/done"},
		{http.MethodPost, "api/signin"},
	}

	for _, route := range declared {
		what := route.method + " /" + route.path
		resp := probe(t, route.method, route.path, []byte(`{}`), withToken, withJSON)

		assert.NotEqual(t, http.StatusNotFound, resp.status, "%s must be routed", what)
		assert.NotEqual(t, http.StatusMethodNotAllowed, resp.status, "%s must be routed", what)
		assert.NotEqual(t, notFoundBody, string(resp.body), "%s must not fall through", what)

		// The same route under a prefix must not exist.
		prefixed := probe(t, route.method, "rest/"+route.path, []byte(`{}`), withToken, withJSON)
		assert.NotEqual(t, http.StatusOK, prefixed.status, "/rest/%s must not be routed", route.path)
	}
}

// TestSeamGetFallsBackToStatic pins the decisive routing rule: a GET to a path
// that is registered only for another verb is served by the static file
// handler, and therefore answers 404, not 405. The catch-all in the source is a
// GET registered on every path, so GET never produces a method-not-allowed
// response anywhere in this application.
func TestSeamGetFallsBackToStatic(t *testing.T) {
	for _, path := range []string{
		"api/signin",      // registered POST only
		"api/task/done",   // registered POST only
		"api/task/done/x", // below a registered path
		"api/does-not-exist",
		"nope",
	} {
		resp := probe(t, http.MethodGet, path, nil)
		assertNotFoundEnvelope(t, resp, "GET /"+path)
		assert.Empty(t, resp.header.Values("Allow"), "GET /%s must not advertise Allow", path)
	}
}

// TestSeamMethodNotAllowed pins the method-not-allowed response: status 405, an
// empty body, and one Allow entry per method registered on that path plus the
// GET contributed by the catch-all. The duplicate GET on API paths is part of
// the contract because the catch-all is registered independently of the route.
func TestSeamMethodNotAllowed(t *testing.T) {
	cases := []struct {
		method string
		path   string
		allow  []string
	}{
		{http.MethodPost, "nope", []string{"GET"}},
		{http.MethodPut, "nope", []string{"GET"}},
		{http.MethodPost, "index.html", []string{"GET"}},
		{http.MethodPost, "api/tasks", []string{"GET", "GET"}},
		{http.MethodDelete, "api/tasks", []string{"GET", "GET"}},
		{http.MethodPost, "api/nextdate", []string{"GET", "GET"}},
		{http.MethodPut, "api/task/done", []string{"GET", "POST"}},
		{http.MethodPatch, "api/task", []string{"DELETE", "GET", "GET", "POST", "PUT"}},
		{http.MethodHead, "api/tasks", []string{"GET", "GET"}},
	}

	for _, c := range cases {
		what := c.method + " /" + c.path
		resp := probe(t, c.method, c.path, nil)

		assert.Equal(t, http.StatusMethodNotAllowed, resp.status, what)
		assert.Empty(t, resp.body, "%s must answer with an empty body", what)
		assert.Equal(t, c.allow, allowSet(resp.header), "%s Allow multiset", what)
		if c.method != http.MethodHead {
			// The client drops Content-Length from a HEAD response, so only
			// the methods that carry a body can assert on it.
			assert.Equal(t, "0", resp.header.Get("Content-Length"), what)
		}
	}
}

// TestSeamNotFoundEnvelope pins the bytes of the fallback response, including
// the trailing newline and the nosniff header. Frameworks disagree on all
// three of status text, charset and trailing newline here.
func TestSeamNotFoundEnvelope(t *testing.T) {
	for _, path := range []string{"api/", "api", "api/TASKS", "api/tasks/", "api/task/"} {
		assertNotFoundEnvelope(t, probe(t, http.MethodGet, path, nil, withToken), "GET /"+path)
	}
}

// TestSeamNoPathRewriting checks that the router neither redirects a trailing
// slash to the bare path nor repairs a path by cleaning it. Both behaviours are
// on by default in some routers and would turn a 404 into a 301.
func TestSeamNoPathRewriting(t *testing.T) {
	for _, path := range []string{"api/tasks/", "api/task/", "api/nextdate/"} {
		resp := probe(t, http.MethodGet, path, nil, withToken)
		assert.Equal(t, http.StatusNotFound, resp.status, "GET /%s must not redirect", path)
		assert.Empty(t, resp.header.Get("Location"), "GET /%s must not redirect", path)
	}
}

// TestSeamStaticAssets pins the static file seam: the redirects the file
// handler generates, and the content types it reports. The stylesheet content
// type is overridden by the application and carries no charset, unlike every
// other asset.
func TestSeamStaticAssets(t *testing.T) {
	redirects := map[string]string{
		"index.html":     "./",
		"web/index.html": "./",
		"css":            "css/",
	}
	for path, location := range redirects {
		resp := probe(t, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusMovedPermanently, resp.status, "GET /"+path)
		assert.Equal(t, location, resp.header.Get("Location"), "GET /"+path)
	}

	assets := []struct {
		path        string
		contentType string
		size        int
	}{
		{"", "text/html; charset=utf-8", 4207},
		{"login.html", "text/html; charset=utf-8", 2397},
		{"css/style.css", "text/css", 2678},
		{"css/theme.css", "text/css", 16371},
		{"js/scripts.min.js", "text/javascript; charset=utf-8", 66461},
		{"favicon.ico", "image/x-icon", 4286},
	}
	for _, a := range assets {
		resp := probe(t, http.MethodGet, a.path, nil)
		assert.Equal(t, http.StatusOK, resp.status, "GET /"+a.path)
		assert.Equal(t, a.contentType, resp.contentType(), "GET /"+a.path)
		assert.Len(t, resp.body, a.size, "GET /"+a.path)
		assert.Equal(t, "bytes", resp.header.Get("Accept-Ranges"), "GET /"+a.path)
	}
}

// TestSeamSuccessContentTypes pins the content types of successful API
// responses. The handlers write these headers by hand and none of them carries
// a charset; a router helper that serialises on the application's behalf would
// append one.
func TestSeamSuccessContentTypes(t *testing.T) {
	next := probe(t, http.MethodGet, "api/nextdate?now=20240126&date=20240126&repeat=d+1", nil)
	assert.Equal(t, http.StatusOK, next.status)
	assert.Equal(t, "text/plain", next.contentType(), "next date must not gain a charset")
	assert.Equal(t, "20240126", string(next.body), "next date must not gain a trailing newline")

	tasks := probe(t, http.MethodGet, "api/tasks", nil, withToken)
	assert.Equal(t, http.StatusOK, tasks.status)
	assert.Equal(t, "application/json", tasks.contentType(), "task list must not gain a charset")
	assert.True(t, bytes.HasPrefix(tasks.body, []byte(`{"tasks":[`)),
		"task list must serialise an empty list as [], got %s", tasks.body)
}

// TestSeamErrorEnvelope pins the shape of application error responses. They are
// JSON documents emitted through the standard library error helper, so they are
// labelled text/plain, carry nosniff, and end in a newline. A migration that
// routes them through a JSON response helper changes all three.
func TestSeamErrorEnvelope(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
		status int
		want   string
	}{
		{"unparseable body", http.MethodPost, "api/task", []byte(`{`), http.StatusBadRequest,
			`{"error":"ошибка декодирования JSON"}` + "\n"},
		{"missing title", http.MethodPost, "api/task", []byte(`{"date":"20240126"}`), http.StatusBadRequest,
			`{"error":"отсутствует обязательное поле title"}` + "\n"},
		{"missing id", http.MethodGet, "api/task", nil, http.StatusBadRequest,
			`{"error":"не передан идентификатор"}` + "\n"},
		{"non-numeric id", http.MethodGet, "api/task?id=abc", nil, http.StatusBadRequest,
			`{"error":"id должен быть числом"}` + "\n"},
		{"unknown id", http.MethodGet, "api/task?id=999999", nil, http.StatusNotFound,
			`{"error":"задача с указанным id не найдена"}` + "\n"},
		{"wrong password", http.MethodPost, "api/signin", []byte(`{"password":"nope"}`), http.StatusUnauthorized,
			`{"error":"неверный пароль"}` + "\n"},
	}

	for _, c := range cases {
		resp := probe(t, c.method, c.path, c.body, withToken, withJSON)
		assert.Equal(t, c.status, resp.status, c.name)
		assert.Equal(t, plainTextUTF8, resp.contentType(), c.name)
		assert.Equal(t, "nosniff", resp.header.Get("X-Content-Type-Options"), c.name)
		assert.Equal(t, c.want, string(resp.body), c.name)
	}
}

// TestSeamAuthRejection pins the authentication seam. Rejections are plain text
// rather than the JSON error envelope used elsewhere, and every protected route
// rejects identically.
func TestSeamAuthRejection(t *testing.T) {
	protected := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "api/task"},
		{http.MethodGet, "api/tasks"},
		{http.MethodGet, "api/task"},
		{http.MethodPut, "api/task"},
		{http.MethodDelete, "api/task"},
		{http.MethodPost, "api/task/done"},
	}

	for _, route := range protected {
		what := route.method + " /" + route.path + " unauthenticated"
		resp := probe(t, route.method, route.path, []byte(`{}`), withJSON)
		assert.Equal(t, http.StatusUnauthorized, resp.status, what)
		assert.Equal(t, plainTextUTF8, resp.contentType(), what)
		assert.Equal(t, unauthorizedBody, string(resp.body), what)

		garbage := probe(t, route.method, route.path, []byte(`{}`), withJSON, func(req *http.Request) {
			req.AddCookie(&http.Cookie{Name: "token", Value: "not-a-jwt"})
		})
		assert.Equal(t, http.StatusUnauthorized, garbage.status, what+" with a bad token")
		assert.Equal(t, unauthorizedBody, string(garbage.body), what+" with a bad token")
	}

	// The unauthenticated route stays unauthenticated.
	open := probe(t, http.MethodGet, "api/nextdate?now=20240126&date=20240126&repeat=d+1", nil)
	assert.Equal(t, http.StatusOK, open.status, "next date must not require a token")
}

// TestSeamPreservedQuirks pins upstream defects that a migration must carry
// over rather than repair.
func TestSeamPreservedQuirks(t *testing.T) {
	// Deleting a task that does not exist reports a server error, not 404,
	// even though the message says the task was not found.
	del := probe(t, http.MethodDelete, "api/task?id=999999", nil, withToken)
	assert.Equal(t, http.StatusInternalServerError, del.status,
		"deleting an unknown task answers 500 upstream")
	assert.Equal(t, `{"error":"задача с указанным id не найдена"}`+"\n", string(del.body))

	// The rejection message for an unparseable now parameter contains a Latin
	// e in the middle of a Cyrillic word, and is emitted raw rather than as the
	// JSON error envelope used by every other handler.
	now := probe(t, http.MethodGet, "api/nextdate?now=bogus&date=20240126&repeat=d+1", nil)
	assert.Equal(t, http.StatusBadRequest, now.status)
	assert.Equal(t, "Неправильный формат парамeтра now\n", string(now.body))

	// The issued token carries no claims at all, so it never expires.
	signin := probe(t, http.MethodPost, "api/signin", []byte(`{"password":"test12345"}`), withJSON)
	require.Equal(t, http.StatusOK, signin.status)
	assert.Equal(t, "application/json", signin.contentType(), "sign-in must not gain a charset")

	var issued struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(signin.body, &issued))

	segments := strings.Split(issued.Token, ".")
	require.Len(t, segments, 3, "sign-in must return a JWT, got %q", issued.Token)
	claims, err := base64.RawURLEncoding.DecodeString(segments[1])
	require.NoError(t, err)
	assert.Equal(t, "{}", string(claims), "the issued token must carry no claims")
}
