// Witnesses: for every part of the closed list (reading.Parts), a test
// that runs the engine through internal/story's Run and shows it does what
// the sentence says. Each witness names the sentences it relies on, by
// their exact text in the reading of a fixture Type
// (testdata/types/), and the parts those sentences carry; then it runs a
// story, written by hand, whose WHOLE timeline is expected. A witness
// shows both sides where there are two: the named person can, someone else
// is refused with the API's reason.
//
// Witnesses are written, never generated from the Type: a generator that
// read the Type to build the test would share the renderer's misreading.
package reading_test

import (
	"context"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/story"
	"github.com/francoisnyc/work-kernel/internal/testdb"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// witness is one test of what sentences say.
type witness struct {
	name string
	// parts are the parts it witnesses; the sentences below must carry
	// every one of them.
	parts []reading.Part
	// sentences are the sentences it relies on, by fixture Type (id@v),
	// each exactly as the reading of that Type writes it.
	sentences map[string][]string
	// story is the story it runs: every line of what happens is expected.
	story string
}

// witnesses is every witness, from the witness_*_test.go files.
var witnesses []witness

func addWitness(w witness) bool {
	witnesses = append(witnesses, w)
	return true
}

var (
	envOnce sync.Once
	envErr  error
	shared  *story.Env
	stopDB  = func() {}
)

func TestMain(m *testing.M) {
	code := m.Run()
	stopDB()
	os.Exit(code)
}

// witnessEnv is the environment every witness shares: one Postgres, with
// the fixture Types registered.
func witnessEnv(t *testing.T) *story.Env {
	t.Helper()
	envOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		pool, stop, err := testdb.Start(ctx)
		if err != nil {
			envErr = err
			return
		}
		stopDB = stop
		shared, envErr = story.NewEnv(ctx, pool, "testdata/types")
		if envErr != nil {
			log.Printf("NewEnv: %v", envErr)
		}
	})
	if envErr != nil {
		t.Fatalf("the test environment: %v", envErr)
	}
	return shared
}

// fixtures are the witness Types, by id@version.
func fixtures(t *testing.T) map[string]*typedef.Type {
	t.Helper()
	types, err := typedef.ParseDir("testdata/types")
	if err != nil {
		t.Fatalf("the fixture Types: %v", err)
	}
	out := map[string]*typedef.Type{}
	for _, ty := range types {
		out[ty.ID+"@"+strconv.Itoa(ty.Version)] = ty
	}
	return out
}

// checkSentences holds a witness's sentences to the readings of its
// fixtures: each is there, word for word, and together they carry every
// part the witness claims.
func checkSentences(t *testing.T, w witness, types map[string]*typedef.Type) {
	t.Helper()
	carried := map[reading.Part]bool{}
	for name, texts := range w.sentences {
		ty, ok := types[name]
		if !ok {
			t.Errorf("no fixture Type %s", name)
			continue
		}
		r := reading.Read(ty, contracts.Default())
		for _, text := range texts {
			s, ok := findSentence(r, text)
			if !ok {
				t.Errorf("the reading of %s has no sentence\n  %s\nIt reads:\n%s", name, text, r.Text())
				continue
			}
			for _, p := range s.Parts {
				carried[p] = true
			}
		}
	}
	for _, p := range w.parts {
		if !carried[p] {
			t.Errorf("no sentence of the witness carries the part %s", p)
		}
	}
}

func findSentence(r *reading.Reading, text string) (reading.Sentence, bool) {
	for _, sec := range r.Sections {
		for _, s := range sec.Sentences {
			if s.Text == text {
				return s, true
			}
		}
	}
	return reading.Sentence{}, false
}

// runStory runs a witness's story and fails the test on any difference
// from its expected timeline, with the story package's report.
func runStory(t *testing.T, w witness) {
	t.Helper()
	s, err := story.Parse("witness "+w.name, []byte(w.story))
	if err != nil {
		t.Fatalf("the story: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := story.Run(ctx, witnessEnv(t), s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	d := story.Compare(s.Expect, res.Timeline)
	if len(res.Failures) > 0 || !d.Empty() {
		t.Errorf("the story:\n%s", story.Report(s, res, d))
	}
}

// TestWitnesses runs every witness: its sentences are in the readings, and
// its story does what they say.
func TestWitnesses(t *testing.T) {
	types := fixtures(t)
	for _, w := range witnesses {
		t.Run(w.name, func(t *testing.T) {
			checkSentences(t, w, types)
			runStory(t, w)
		})
	}
}

// TestEveryPartHasAWitness holds the witnesses to the closed list of
// parts, both ways: each part has a witness, and each witness names parts
// that exist.
func TestEveryPartHasAWitness(t *testing.T) {
	all := reading.Parts()
	witnessed := map[reading.Part][]string{}
	for _, w := range witnesses {
		if len(w.parts) == 0 {
			t.Errorf("witness %s names no part", w.name)
		}
		for _, p := range w.parts {
			if !slices.Contains(all, p) {
				t.Errorf("witness %s names %q, which is no part", w.name, p)
			}
			witnessed[p] = append(witnessed[p], w.name)
		}
	}
	for _, p := range all {
		if len(witnessed[p]) == 0 {
			t.Errorf("part %s has no witness", p)
		}
	}
	names := map[string]bool{}
	for _, w := range witnesses {
		if names[w.name] {
			t.Errorf("two witnesses are named %s", w.name)
		}
		names[w.name] = true
	}
}

// TestParts_Closed: the list has no duplicate, and every part is a kind
// or a clause of a kind on the list (kind.clause).
func TestParts_Closed(t *testing.T) {
	seen := map[reading.Part]bool{}
	for _, p := range reading.Parts() {
		if seen[p] {
			t.Errorf("%s is listed twice", p)
		}
		seen[p] = true
	}
	for _, p := range reading.Parts() {
		kind, _, isClause := strings.Cut(string(p), ".")
		if !isClause {
			continue
		}
		if !seen[reading.Part(kind)] && !slices.Contains([]string{"who", "except", "reaction", "created_inside", "consent", "placement"}, kind) {
			t.Errorf("%s is a clause of %s, which is no kind", p, kind)
		}
	}
}
