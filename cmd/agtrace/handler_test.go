package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dongss/agent-trace/internal/agent"
)

// sessionID is the uuid the fixture transcript is filed under. Discovery takes
// the id from the file name, so the two have to agree.
const sessionID = "abcdef12-3456-7890-abcd-ef1234567890"

// fixtureRoot writes one transcript into the layout Discover expects —
// <root>/<cwd-slug>/<session-uuid>.jsonl — and returns an agent pointed at it.
// The handlers then read a directory this test made rather than whatever the
// machine happens to have, so they behave the same on a developer's laptop and
// on a runner with no Claude Code installed.
func fixtureRoot(t *testing.T) agent.Agent {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "-Users-someone-workspace-app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const usage = `"usage":{"input_tokens":2,"cache_creation_input_tokens":300,` +
		`"cache_read_input_tokens":1000,"output_tokens":50,` +
		`"output_tokens_details":{"thinking_tokens":20}}`
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-09-18T10:00:00.000Z",` +
			`"cwd":"/Users/someone/workspace/app","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-09-18T10:00:01.000Z","message":` +
			`{"id":"m1","model":"claude-opus-5","role":"assistant",` +
			`"content":[{"type":"text","text":"hello"}],` + usage + `}}`,
		`{"type":"ai-title","sessionId":"s","aiTitle":"a fixture session"}`,
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := agent.Default()
	a.Root = root
	return a
}

func get(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

// Every route, and what it answers with. These ran only as hand-typed curl
// before, which is to say they ran when somebody remembered.
func TestRoutes(t *testing.T) {
	mux := newMux(fixtureRoot(t))

	for _, tc := range []struct {
		name   string
		target string
		want   int
	}{
		{"listing", "/", http.StatusOK},
		{"session", "/s/" + sessionID, http.StatusOK},
		{"session by prefix", "/s/" + sessionID[:8], http.StatusOK},
		{"windowed session", "/s/" + sessionID + "?from=2026-09-18&to=2026-09-19", http.StatusOK},
		{"filtered listing", "/?q=fixture&sort=cost&dir=desc", http.StatusOK},

		{"unknown path", "/nope", http.StatusNotFound},
		{"unknown session", "/s/nosuchsession", http.StatusNotFound},
		{"no session named", "/s/", http.StatusNotFound},

		// A bound that cannot be read is the caller's mistake, not ours, and
		// silently ignoring it would render a window nobody asked for.
		{"unreadable from", "/s/" + sessionID + "?from=notatime", http.StatusBadRequest},
		{"unreadable to", "/s/" + sessionID + "?to=nope", http.StatusBadRequest},
		{"unknown agent", "/?agent=nosuchcli", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := get(t, mux, tc.target).Code; got != tc.want {
				t.Errorf("GET %s = %d, want %d", tc.target, got, tc.want)
			}
		})
	}
}

// Both pages are built fresh from the transcript on every request, so a
// session still being written shows its latest state on reload. A cached one
// would show yesterday's run and give no sign of it.
func TestPagesAreNeverCached(t *testing.T) {
	mux := newMux(fixtureRoot(t))
	for _, target := range []string{"/", "/s/" + sessionID} {
		h := get(t, mux, target).Header()
		if got := h.Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", target, got)
		}
		if got := h.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("GET %s: Content-Type = %q", target, got)
		}
	}
}

// The listing has to render on a machine that has never run the agent CLI —
// which is what a fresh runner is — and say which of "no reader" and "nothing
// to read" it means rather than showing an empty table.
func TestListingWithNoTranscripts(t *testing.T) {
	a := agent.Default()
	a.Root = filepath.Join(t.TempDir(), "nothing-here")

	w := get(t, newMux(a), "/")
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "No transcripts on this machine") {
		t.Error("the listing does not say why it is empty")
	}
	if strings.Contains(body, "<tbody>") {
		t.Error("an empty listing still drew a table")
	}
}

// The transcript reaches the page through one JSON payload, and the page is
// self-contained. Both are asserted in internal/render; this checks that what
// the server actually writes is that page and not a stub.
func TestSessionPageCarriesTheRun(t *testing.T) {
	w := get(t, newMux(fixtureRoot(t)), "/s/"+sessionID)
	body := w.Body.String()
	for _, want := range []string{
		`id="agtrace-data"`, // the payload
		"a fixture session", // the title the transcript states
		"~/workspace/app",   // the cwd, with the home collapsed
		"claude-opus-5",     // the model that answered
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the session page does not carry %q", want)
		}
	}
}
