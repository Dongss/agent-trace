package agent

import (
	"strings"
	"testing"
)

// Only agents with a reader are listed: the page must never
// offer an option that returns nothing.
func TestListOnlyHasReadableAgents(t *testing.T) {
	agents := List()
	if len(agents) == 0 {
		t.Fatal("no agent can be read at all")
	}
	for _, a := range agents {
		if a.ID == "" || a.Name == "" || a.Transcripts == "" {
			t.Errorf("agent with an empty id, name or transcript location: %+v", a)
		}
		if !a.Implemented || a.discover == nil || a.load == nil {
			t.Errorf("%s is listed without a working reader", a.ID)
		}
	}
}

func TestDefaultIsListed(t *testing.T) {
	d := Default()
	for _, a := range List() {
		if a.ID == d.ID {
			return
		}
	}
	t.Errorf("default agent %q is not in List", d.ID)
}

func TestLookup(t *testing.T) {
	if a, err := Lookup(""); err != nil || a.ID != Default().ID {
		t.Errorf("empty id should give the default, got %q / %v", a.ID, err)
	}
	if a, err := Lookup("claude-code"); err != nil || a.ID != "claude-code" {
		t.Errorf("Lookup(claude-code) = %q / %v", a.ID, err)
	}
	_, err := Lookup("nope")
	if err == nil {
		t.Fatal("an unknown agent was accepted")
	}
	// The error names the alternatives rather than just refusing.
	for _, want := range []string{"nope", "claude-code"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// An entry without a reader — should one ever be constructed — fails with the
// reason rather than returning an empty list that reads as "no sessions".
func TestAgentWithoutReaderExplainsItself(t *testing.T) {
	a := Agent{ID: "x", Name: "X", Why: "not written yet"}
	if _, err := a.Discover(); err == nil || !strings.Contains(err.Error(), "not written yet") {
		t.Errorf("Discover err = %v", err)
	}
	if _, err := a.Load("ref"); err == nil || !strings.Contains(err.Error(), "X") {
		t.Errorf("Load err = %v", err)
	}
	if got := a.Status(); got != a.Why {
		t.Errorf("Status() = %q, want %q", got, a.Why)
	}
	// And List never hands one out.
	for _, l := range List() {
		if l.discover == nil {
			t.Errorf("List returned %s without a reader", l.ID)
		}
	}
}

func TestStatusDistinguishesNoReaderFromNoData(t *testing.T) {
	a := Default()
	a.Root = "/nonexistent-for-this-test"
	if got := a.Status(); !strings.Contains(got, "no transcripts") {
		t.Errorf("Status with a missing root = %q; a reader with no data is not the same as no reader", got)
	}
}
