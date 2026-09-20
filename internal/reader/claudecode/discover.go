package claudecode

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dongss/agent-trace/internal/textfmt"
)

// DefaultRoot is where Claude Code keeps its transcripts: one directory per
// working directory, one file per session.
func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// Session is one transcript as the session list shows it, filled from file
// metadata plus a bounded peek at each end of the file.
//
// Listing does not parse whole transcripts: the sessions on the survey machine
// run to tens of thousands of lines each, and a listing that costs a full
// parse per row is a listing nobody waits for.
type Session struct {
	Path    string // real path, for opening
	Display string // path with the home directory collapsed, for showing
	ID      string // session uuid, from the file name
	CWD     string
	Version string
	Size    int64
	ModTime time.Time

	First, Last time.Time
	HasFirst    bool
	HasLast     bool

	// CostUSD is what the transcript's own last cost snapshot claims. It is
	// absent in a session that never recorded one, which is why it is a
	// pointer rather than a zero.
	CostUSD *float64

	// Totals is nil until Scan has read the transcript. Listing metadata comes
	// from the ends of the file; a title and a token count do not, so they are
	// a separate and more expensive step, and the caller decides when to pay
	// for it.
	Totals *Totals
}

// Title is the name the CLI wrote for the session, or "" when it wrote none —
// which was 29 of the 95 sessions on the survey machine, mostly short ones
// where no response was ever produced to summarise.
//
// It deliberately does not fall back to the working directory. A listing shows
// the working directory in its own column, so repeating its last element under
// "Title" filled the space without adding anything, and made a derived label
// indistinguishable from one the CLI actually wrote.
func (s Session) Title() string {
	if s.Totals != nil {
		return s.Totals.Title
	}
	return ""
}

// Name is what the CLI calls this session, or "" when it never said. It is a
// different field from Title and the two often disagree: a session named
// "webapp" can carry a title the model wrote about one afternoon in it.
func (s Session) Name() string {
	if s.Totals != nil {
		return s.Totals.AgentName
	}
	return ""
}

// Discover lists the transcripts under root, most recently modified first.
func Discover(root string) ([]Session, error) {
	if root == "" {
		return nil, os.ErrNotExist
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	var out []Session
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue // a directory that vanished or cannot be read is not fatal to a listing
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			path := filepath.Join(root, d.Name(), f.Name())
			s := Session{
				Path:    path,
				Display: textfmt.Path(path),
				ID:      strings.TrimSuffix(f.Name(), ".jsonl"),
				Size:    info.Size(),
				ModTime: info.ModTime(),
			}
			peek(&s)
			out = append(out, s)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// peek fills the fields that can be had cheaply: the head of the file for the
// working directory and the first timestamp, the tail for the last timestamp
// and the last cost snapshot. Failures are silent — a row with holes in it is
// more useful than no row.
func peek(s *Session) {
	f, err := os.Open(s.Path)
	if err != nil {
		return
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 1<<16)
	for i := 0; i < 64; i++ {
		line, err := br.ReadString('\n')
		if line != "" {
			var e entry
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &e) == nil {
				if s.CWD == "" && e.CWD != "" {
					s.CWD = textfmt.Path(e.CWD)
				}
				if s.Version == "" && e.Version != "" {
					s.Version = e.Version
				}
				if !s.HasFirst && e.Timestamp != "" {
					if t, perr := time.Parse(time.RFC3339Nano, e.Timestamp); perr == nil {
						s.First, s.HasFirst = t, true
					}
				}
			}
		}
		if err != nil || (s.CWD != "" && s.HasFirst && s.Version != "") {
			break
		}
	}

	for _, line := range tailLines(f, s.Size, 1<<18) {
		var e entry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.Type == "cost-state" {
			c := e.TotalCostUSD
			s.CostUSD = &c
		}
		if e.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil && t.After(s.Last) {
				s.Last, s.HasLast = t, true
			}
		}
	}
}

// tailLines returns the complete lines in the last n bytes of the file. The
// first line of the window is dropped unless the window covers the whole file,
// because it is almost certainly cut in half.
func tailLines(f *os.File, size, n int64) []string {
	if size == 0 {
		return nil
	}
	start := size - n
	whole := start <= 0
	if whole {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(buf), "\n")
	if !whole && len(lines) > 0 {
		lines = lines[1:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Locate resolves what a user typed into a transcript path: a path as given, or
// a session id (or unique prefix of one) to look up under root.
func Locate(root, ref string) (string, error) {
	if ref == "" {
		return "", os.ErrNotExist
	}
	if st, err := os.Stat(ref); err == nil && !st.IsDir() {
		return ref, nil
	}
	sessions, err := Discover(root)
	if err != nil {
		return "", err
	}
	var hits []string
	for _, s := range sessions {
		if s.ID == ref {
			return s.Path, nil
		}
		if strings.HasPrefix(s.ID, ref) {
			hits = append(hits, s.Path)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", os.ErrNotExist
	default:
		return "", errAmbiguous(hits)
	}
}

type errAmbiguous []string

func (e errAmbiguous) Error() string {
	names := make([]string, 0, len(e))
	for _, p := range e {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".jsonl"))
	}
	sort.Strings(names)
	if len(names) > 6 {
		names = append(names[:6], "…")
	}
	return "matches several sessions: " + strings.Join(names, ", ")
}
