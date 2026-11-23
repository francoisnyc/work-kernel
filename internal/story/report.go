package story

import (
	"fmt"
	"strings"
	"time"
)

// Report says, for a person, how a run went against its story: its
// failures, then how the timeline differs, instant by instant.
func Report(s *Story, r *Result, d Diff) string {
	var b strings.Builder
	fmt.Fprintf(&b, "story %s: %s\n", s.Source, s.Name)
	for _, f := range r.Failures {
		fmt.Fprintf(&b, "%s:%d: %s\n", s.Source, f.Line, f.Message)
	}
	if d.Empty() {
		fmt.Fprintf(&b, "the timeline is as expected (%d lines), to %s\n", d.Matched, FormatTime(s.Day1, r.Ended))
		return b.String()
	}
	fmt.Fprintf(&b, "the timeline differs (%d lines matched; - expected and missing, + seen and not expected), to %s:\n",
		d.Matched, FormatTime(s.Day1, r.Ended))
	type group struct {
		heading     string
		at          time.Time
		minus, plus []Line
	}
	var groups []*group
	find := func(heading string, at time.Time) *group {
		for _, g := range groups {
			if g.heading == heading {
				return g
			}
		}
		g := &group{heading: heading, at: at}
		groups = append(groups, g)
		return g
	}
	for _, l := range d.Missing {
		h := FormatTime(s.Day1, l.At)
		if !l.To.IsZero() {
			h = "between " + h + " and " + FormatTime(s.Day1, l.To)
		}
		g := find(h, l.At)
		g.minus = append(g.minus, l)
	}
	for _, l := range d.Unexpected {
		g := find(FormatTime(s.Day1, l.At), l.At)
		g.plus = append(g.plus, l)
	}
	// In time order, a window before the instants it starts on.
	for i := 1; i < len(groups); i++ {
		for j := i; j > 0 && groups[j].at.Before(groups[j-1].at); j-- {
			groups[j], groups[j-1] = groups[j-1], groups[j]
		}
	}
	for _, g := range groups {
		fmt.Fprintf(&b, "%s\n", g.heading)
		for _, l := range g.minus {
			where := ""
			if l.Line > 0 {
				where = fmt.Sprintf("   (%s:%d)", s.Source, l.Line)
			}
			fmt.Fprintf(&b, "  - %s: %s%s\n", l.Item, l.Text, where)
		}
		for _, l := range g.plus {
			fmt.Fprintf(&b, "  + %s: %s\n", l.Item, l.Text)
		}
	}
	return b.String()
}
