package claudecode

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
)

// activityOf is what Totals.Activity has to be, worked out from the full
// parse: each response's usage in the slot of the step it is attached to.
func activityOf(run *event.Run) map[int64]Slot {
	out := map[int64]Slot{}
	for i := range run.Steps {
		st := &run.Steps[i]
		if st.Usage == nil || !st.HasTime {
			continue
		}
		k := SlotOf(st.At)
		sl := out[k]
		sl.Responses++
		addUsage(&sl.Tokens, *st.Usage)
		out[k] = sl
	}
	return out
}

func slotsOf(tot *Totals) map[int64]Slot {
	out := map[int64]Slot{}
	for k, v := range tot.Activity {
		out[k] = *v
	}
	return out
}

// The listing's heatmap draws Scan's slots, and the session page counts the
// full parse's responses; the two must put every response in the same place.
// A response split over entries keeps its first entry's slot even when a
// later entry, in the next slot, raises its output, and one with no timestamp
// is in the totals but in no slot.
func TestScanActivityAgreesWithTheFullParser(t *testing.T) {
	const usage = `"usage":{"input_tokens":2,"cache_creation_input_tokens":10,"cache_read_input_tokens":100,"output_tokens":%s}`
	resp := func(uuid, ts, id, out string) string {
		tsField := ""
		if ts != "" {
			tsField = `"timestamp":"` + ts + `",`
		}
		return `{"type":"assistant","uuid":"` + uuid + `",` + tsField + `"message":{"id":"` + id +
			`","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"x"}],` +
			strings.Replace(usage, "%s", out, 1) + `}}`
	}
	lines := []string{
		resp("a1", "2026-09-18T10:14:59.900Z", "m1", "5"),
		resp("a2", "2026-09-18T10:15:00.000Z", "m2", "1"),
		resp("a3", "2026-09-18T10:31:00.000Z", "m2", "300"), // m2 climbs, in m2's first slot
		resp("a4", "", "m3", "7"),                           // no timestamp: totals only
		resp("a5", "2026-09-18T12:00:00.000Z", "m4", "9"),
	}
	path := t.TempDir() + "/s.jsonl"
	if err := writeFile(path, strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	tot, err := Scan(path)
	if err != nil {
		t.Fatal(err)
	}
	run, err := ReadFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got, want := slotsOf(tot), activityOf(run)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scan slots %+v, full parse %+v", got, want)
	}
	at := func(s string) int64 {
		tm, _ := time.Parse(time.RFC3339, s)
		return tm.Unix()
	}
	for k, w := range map[int64]struct{ responses, output int }{
		at("2026-09-18T10:00:00Z"): {1, 5},
		at("2026-09-18T10:15:00Z"): {1, 300},
		at("2026-09-18T12:00:00Z"): {1, 9},
	} {
		if s := got[k]; s.Responses != w.responses || s.Tokens.Output != w.output {
			t.Errorf("slot %d: %d responses, output %d; want %d, %d", k, s.Responses, s.Tokens.Output, w.responses, w.output)
		}
	}
	if len(got) != 3 {
		t.Errorf("%d slots, want 3", len(got))
	}
	if tot.Responses != 4 {
		t.Errorf("%d responses in the totals, want 4 with the untimed one", tot.Responses)
	}
}
