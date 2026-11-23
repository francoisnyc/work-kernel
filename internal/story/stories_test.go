// TestStories runs every story in stories/ against the real kernel in
// simulated time, and fails with a report a person can read. Run one with
// make stories STORY=vacation/approve (go test -run 'TestStories/vacation/approve').
package story_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/story"
)

type storyFile struct {
	name string // domain/name, the subtest's name
	path string
	data []byte
}

// storyFiles reads stories/<domain>/<name>.yaml.
func storyFiles(t *testing.T) []storyFile {
	t.Helper()
	paths, err := filepath.Glob("../../stories/*/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 6 {
		t.Fatalf("found %d stories in stories/; want at least the first six", len(paths))
	}
	var files []storyFile
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel("../../stories", p)
		files = append(files, storyFile{name: strings.TrimSuffix(filepath.ToSlash(rel), ".yaml"), path: filepath.ToSlash(p), data: data})
	}
	return files
}

func TestStories(t *testing.T) {
	env := story.TestEnv(t)
	for _, f := range storyFiles(t) {
		t.Run(f.name, func(t *testing.T) {
			s, err := story.Parse(f.path, f.data)
			if err != nil {
				t.Fatalf("%v", err)
			}
			began := time.Now()
			res, err := story.Run(story.RunCtx(t), env, s)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			t.Logf("%s: %d lines, ran to %s in %v", f.name, len(res.Timeline), story.FormatTime(s.Day1, res.Ended), time.Since(began))
			d := story.Compare(s.Expect, res.Timeline)
			if len(res.Failures) > 0 || !d.Empty() {
				t.Errorf("\n%s", story.Report(s, res, d))
			}
		})
	}
}
