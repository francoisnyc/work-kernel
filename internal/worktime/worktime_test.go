// Tests for Jira part 2, step 3: working-time arithmetic (docs/design.md,
// "Decisions for Jira part 2", primitive 5).
//
// A calendar is a week schedule in local wall-clock time, a list of
// holidays (local dates) and an IANA zone. Two questions:
//
//   - After(t, d): the instant at which d of working time has passed since
//     t. It is the EARLIEST such instant: eight hours after Monday's opening
//     is Monday's closing, not Tuesday's opening.
//   - Between(t1, t2): the working time between two instants.
//
// Every instant below is written in UTC, with the local time beside it, so
// the tests read the same on any machine: the package may not depend on the
// machine's zone or clock.
package worktime_test

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/worktime"
)

// Monday to Friday, nine to five.
var officeWeek = []string{"mon 09:00-17:00", "tue 09:00-17:00", "wed 09:00-17:00", "thu 09:00-17:00", "fri 09:00-17:00"}

// Every hour of every day.
var alwaysWeek = []string{"mon 00:00-24:00", "tue 00:00-24:00", "wed 00:00-24:00", "thu 00:00-24:00",
	"fri 00:00-24:00", "sat 00:00-24:00", "sun 00:00-24:00"}

func cal(t *testing.T, week, holidays []string, zone string) *worktime.Calendar {
	t.Helper()
	c, err := worktime.New(week, holidays, zone)
	if err != nil {
		t.Fatalf("New(%v, %v, %s): %v", week, holidays, zone, err)
	}
	return c
}

// utc reads "2026-11-23 09:00" or "2026-11-23 09:00:05" as UTC.
func utc(t *testing.T, s string) time.Time {
	t.Helper()
	layout := "2006-01-02 15:04"
	if len(s) > len(layout) {
		layout = "2006-01-02 15:04:05"
	}
	v, err := time.Parse(layout, s)
	if err != nil {
		t.Fatalf("bad test instant %q: %v", s, err)
	}
	return v
}

type afterCase struct {
	name  string
	from  string // UTC
	d     time.Duration
	want  string // UTC
	local string // the same, as the calendar's wall clock reads it
}

func runAfter(t *testing.T, c *worktime.Calendar, cases []afterCase) {
	t.Helper()
	for _, tc := range cases {
		got, err := c.After(utc(t, tc.from), tc.d)
		if err != nil {
			t.Errorf("%s: After(%s, %v): %v", tc.name, tc.from, tc.d, err)
			continue
		}
		if want := utc(t, tc.want); !got.Equal(want) {
			t.Errorf("%s: After(%s, %v) = %s, want %s (%s)", tc.name, tc.from, tc.d,
				got.UTC().Format(time.RFC3339), want.Format(time.RFC3339), tc.local)
		}
	}
}

// Paris in winter is UTC+1. 2026-11-23 is a Monday.
func TestAfter_BusinessHours(t *testing.T) {
	c := cal(t, officeWeek, nil, "Europe/Paris")
	runAfter(t, c, []afterCase{
		{"inside one day", "2026-11-23 09:00", 2 * time.Hour, "2026-11-23 11:00", "Mon 10:00 + 2h = Mon 12:00"},
		{"over the night", "2026-11-23 15:00", 2 * time.Hour, "2026-11-24 09:00", "Mon 16:00 + 2h = Tue 10:00"},
		{"before opening", "2026-11-23 06:00", time.Hour, "2026-11-23 09:00", "Mon 07:00 + 1h = Mon 10:00"},
		{"after closing", "2026-11-23 19:00", time.Hour, "2026-11-24 09:00", "Mon 20:00 + 1h = Tue 10:00"},
		{"a whole day ends at closing", "2026-11-23 08:00", 8 * time.Hour, "2026-11-23 16:00", "Mon 09:00 + 8h = Mon 17:00, not Tue 09:00"},
		{"at closing, the next day counts", "2026-11-23 16:00", time.Hour, "2026-11-24 09:00", "Mon 17:00 + 1h = Tue 10:00"},
		{"two and a half days", "2026-11-23 08:00", 20 * time.Hour, "2026-11-25 12:00", "Mon 09:00 + 20h = Wed 13:00"},
		{"seconds", "2026-11-23 09:00:00", 5 * time.Second, "2026-11-23 09:00:05", "Mon 10:00:00 + 5s"},
		{"seconds over the night", "2026-11-23 15:59:58", 5 * time.Second, "2026-11-24 08:00:03", "Mon 16:59:58 + 5s = Tue 09:00:03"},
	})
}

func TestAfter_Weekend(t *testing.T) {
	c := cal(t, officeWeek, nil, "Europe/Paris")
	runAfter(t, c, []afterCase{
		{"friday afternoon", "2026-11-20 15:00", 2 * time.Hour, "2026-11-23 09:00", "Fri 16:00 + 2h = Mon 10:00"},
		{"saturday", "2026-11-21 11:00", time.Hour, "2026-11-23 09:00", "Sat 12:00 + 1h = Mon 10:00"},
		{"a week of work", "2026-11-20 15:00", 40 * time.Hour, "2026-11-27 15:00", "Fri 16:00 + 40h = next Fri 16:00"},
	})
}

// 2026-12-25 is a Friday. A holiday is a whole local day.
func TestAfter_Holiday(t *testing.T) {
	c := cal(t, officeWeek, []string{"2026-12-25", "2027-01-01"}, "Europe/Paris")
	runAfter(t, c, []afterCase{
		{"over a holiday and a weekend", "2026-12-24 15:00", 2 * time.Hour, "2026-12-28 09:00", "Thu 16:00 + 2h = Mon 10:00"},
		{"asked on the holiday", "2026-12-25 10:00", time.Hour, "2026-12-28 09:00", "Fri (off) 11:00 + 1h = Mon 10:00"},
		{"two holidays apart", "2026-12-24 15:00", 34 * time.Hour, "2027-01-04 09:00", "Thu 16:00 + 1h + 4 days + 1h = Mon 4 Jan 10:00"},
	})
	// The same question without the holidays lands on the Friday.
	plain := cal(t, officeWeek, nil, "Europe/Paris")
	runAfter(t, plain, []afterCase{
		{"no holiday", "2026-12-24 15:00", 2 * time.Hour, "2026-12-25 09:00", "Thu 16:00 + 2h = Fri 10:00"},
	})
}

// Several intervals in one day; lines in any order; intervals that touch.
func TestAfter_SeveralIntervalsADay(t *testing.T) {
	c := cal(t, []string{"mon 13:00-17:00", "mon 09:00-12:00", "tue 09:00-12:00", "tue 12:00-17:00"}, nil, "Europe/Paris")
	runAfter(t, c, []afterCase{
		{"over lunch", "2026-11-23 10:00", 2 * time.Hour, "2026-11-23 13:00", "Mon 11:00 + 2h = Mon 14:00"},
		{"asked at lunch", "2026-11-23 11:30", time.Hour, "2026-11-23 13:00", "Mon 12:30 + 1h = Mon 14:00"},
		{"touching intervals are one", "2026-11-24 10:00", 2 * time.Hour, "2026-11-24 12:00", "Tue 11:00 + 2h = Tue 13:00"},
		{"only two days a week", "2026-11-24 15:00", 2 * time.Hour, "2026-11-30 09:00", "Tue 16:00 + 2h = next Mon 10:00"},
	})
}

// Around the clock, working time is time.
func TestAfter_AlwaysOpen(t *testing.T) {
	c := cal(t, alwaysWeek, nil, "UTC")
	runAfter(t, c, []afterCase{
		{"seconds", "2026-11-21 23:59:58", 5 * time.Second, "2026-11-22 00:00:03", "Sat to Sun"},
		{"days", "2026-11-20 12:00", 72 * time.Hour, "2026-11-23 12:00", "three days"},
	})
}

func TestBetween(t *testing.T) {
	c := cal(t, officeWeek, []string{"2026-12-25"}, "Europe/Paris")
	for _, tc := range []struct {
		name, from, to string
		want           time.Duration
	}{
		{"inside one day", "2026-11-23 09:00", "2026-11-23 11:00", 2 * time.Hour},
		{"over the night", "2026-11-23 15:00", "2026-11-24 09:00", 2 * time.Hour},
		{"over the weekend", "2026-11-20 15:00", "2026-11-23 09:00", 2 * time.Hour},
		{"over the holiday", "2026-12-24 15:00", "2026-12-28 09:00", 2 * time.Hour},
		{"all of it outside working time", "2026-11-21 08:00", "2026-11-22 20:00", 0},
		{"the same instant", "2026-11-23 10:00", "2026-11-23 10:00", 0},
		{"a whole week", "2026-11-23 00:00", "2026-11-30 00:00", 40 * time.Hour},
		{"seconds", "2026-11-23 15:59:58", "2026-11-24 08:00:03", 5 * time.Second},
		{"from before opening to after closing", "2026-11-23 05:00", "2026-11-23 20:00", 8 * time.Hour},
	} {
		got, err := c.Between(utc(t, tc.from), utc(t, tc.to))
		if err != nil || got != tc.want {
			t.Errorf("%s: Between(%s, %s) = %v, %v; want %v", tc.name, tc.from, tc.to, got, err, tc.want)
		}
	}
}

// After and Between are each other's inverse: the working time between t
// and After(t, d) is d.
func TestAfterThenBetween_GivesTheDurationBack(t *testing.T) {
	c := cal(t, officeWeek, []string{"2026-12-25"}, "Europe/Paris")
	for _, from := range []string{"2026-11-20 15:00", "2026-11-21 03:00", "2026-12-24 15:59", "2027-03-26 15:00"} {
		for _, d := range []time.Duration{time.Second, 90 * time.Minute, 8 * time.Hour, 100 * time.Hour} {
			start := utc(t, from)
			due, err := c.After(start, d)
			if err != nil {
				t.Errorf("After(%s, %v): %v", from, d, err)
				continue
			}
			if got, err := c.Between(start, due); err != nil || got != d {
				t.Errorf("Between(%s, After(%s, %v)) = %v, %v; want %v", from, from, d, got, err, d)
			}
		}
	}
}

// Daylight saving time. The week is in wall-clock time, so the UTC hour of
// "ten in the morning" moves with it.
func TestAfter_AcrossADSTChange_KeepsTheWallClock(t *testing.T) {
	// Paris goes from UTC+1 to UTC+2 on Sunday 2027-03-28.
	c := cal(t, officeWeek, nil, "Europe/Paris")
	runAfter(t, c, []afterCase{
		{"spring", "2027-03-26 15:00", 2 * time.Hour, "2027-03-29 08:00", "Fri 16:00 CET + 2h = Mon 10:00 CEST"},
		// ... and back to UTC+1 on Sunday 2027-10-31.
		{"autumn", "2027-10-29 14:00", 2 * time.Hour, "2027-11-01 09:00", "Fri 16:00 CEST + 2h = Mon 10:00 CET"},
	})
}

// A night shift over the change itself: 01:00 to 04:00 on Sundays. In
// spring the hour from 02:00 to 03:00 does not exist, so the shift is two
// hours long; in autumn it happens twice, and both are worked: four hours.
func TestWorkingTime_OnTheNightOfADSTChange(t *testing.T) {
	night := []string{"sun 01:00-04:00"}
	for _, tc := range []struct {
		name, zone string
		from, to   string // UTC, around the whole shift
		want       time.Duration
		// 90 minutes of work from the shift's opening end here.
		opening, after90 string
	}{
		{"Paris, spring forward", "Europe/Paris", "2027-03-27 12:00", "2027-03-28 12:00", 2 * time.Hour,
			"2027-03-28 00:00", "2027-03-28 01:30"}, // 01:00 CET ... 03:30 CEST
		{"Paris, fall back", "Europe/Paris", "2027-10-30 12:00", "2027-10-31 12:00", 4 * time.Hour,
			"2027-10-30 23:00", "2027-10-31 00:30"}, // 01:00 CEST ... 02:30 CEST (the first 02:30)
		{"New York, spring forward", "America/New_York", "2027-03-13 12:00", "2027-03-14 12:00", 2 * time.Hour,
			"2027-03-14 06:00", "2027-03-14 07:30"}, // 01:00 EST ... 03:30 EDT
		{"New York, fall back", "America/New_York", "2027-11-06 12:00", "2027-11-07 12:00", 4 * time.Hour,
			"2027-11-07 05:00", "2027-11-07 06:30"}, // 01:00 EDT ... 01:30 EST (the second 01:30)
		{"an ordinary Sunday", "Europe/Paris", "2027-03-20 12:00", "2027-03-21 12:00", 3 * time.Hour,
			"2027-03-21 00:00", "2027-03-21 01:30"},
	} {
		c := cal(t, night, nil, tc.zone)
		if got, err := c.Between(utc(t, tc.from), utc(t, tc.to)); err != nil || got != tc.want {
			t.Errorf("%s: the shift is %v long, %v; want %v", tc.name, got, err, tc.want)
		}
		got, err := c.After(utc(t, tc.opening), 90*time.Minute)
		if err != nil || !got.Equal(utc(t, tc.after90)) {
			t.Errorf("%s: 90 minutes into the shift = %v, %v; want %s UTC", tc.name, got.UTC(), err, tc.after90)
		}
	}
}

// East of UTC the local day starts before the UTC day: Monday 08:00 in
// Tokyo (UTC+9, no DST) is still Sunday in UTC. Days and holidays are the
// calendar's, not UTC's.
func TestAfter_AZoneEastOfUTC(t *testing.T) {
	c := cal(t, officeWeek, nil, "Asia/Tokyo")
	runAfter(t, c, []afterCase{
		{"monday morning is sunday in UTC", "2026-11-22 23:00", time.Hour, "2026-11-23 01:00", "Mon 08:00 + 1h = Mon 10:00 JST"},
		{"friday evening is friday morning in UTC", "2026-11-20 07:00", 2 * time.Hour, "2026-11-23 01:00", "Fri 16:00 + 2h = Mon 10:00 JST"},
	})
	// Monday 2026-11-23 is a holiday in Tokyo: the local Monday, which
	// begins on the UTC Sunday.
	h := cal(t, officeWeek, []string{"2026-11-23"}, "Asia/Tokyo")
	runAfter(t, h, []afterCase{
		{"a holiday is a local day", "2026-11-22 23:00", time.Hour, "2026-11-24 01:00", "Mon (off) 08:00 + 1h = Tue 10:00 JST"},
	})
	// West of UTC, the other way: Friday 20:00 in Los Angeles (UTC-8) is
	// Saturday in UTC, and it is not a working time anyway; Friday 16:00
	// is Saturday 00:00 UTC, and it is.
	la := cal(t, officeWeek, nil, "America/Los_Angeles")
	runAfter(t, la, []afterCase{
		{"friday afternoon is saturday in UTC", "2026-11-21 00:00", 30 * time.Minute, "2026-11-21 00:30", "Fri 16:00 + 30m PST"},
	})
}

// The answer does not depend on the Location the instant arrives in.
func TestAfter_TheSameInstantInAnyLocation(t *testing.T) {
	c := cal(t, officeWeek, nil, "Europe/Paris")
	start := utc(t, "2026-11-20 15:00")
	want, err := c.After(start, 2*time.Hour)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	for _, loc := range []*time.Location{time.FixedZone("far east", 14*3600), time.FixedZone("far west", -11*3600)} {
		if got, err := c.After(start.In(loc), 2*time.Hour); err != nil || !got.Equal(want) {
			t.Errorf("asked in %s: %v, %v; want %v", loc, got, err, want)
		}
	}
}

func TestNew_RefusesWhatIsNoCalendar(t *testing.T) {
	many := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, time.Date(2027, 1, 1+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02"))
		}
		return out
	}
	// 22 intervals that do not overlap: four a day for five days, two more.
	var week22 []string
	for _, d := range []string{"mon", "tue", "wed", "thu", "fri"} {
		week22 = append(week22, d+" 01:00-02:00", d+" 03:00-04:00", d+" 05:00-06:00", d+" 07:00-08:00")
	}
	week22 = append(week22, "sat 01:00-02:00", "sat 03:00-04:00")

	for name, c := range map[string]struct {
		week, holidays []string
		zone           string
	}{
		"no working time":            {nil, nil, "UTC"},
		"a day spelled out":          {[]string{"monday 09:00-17:00"}, nil, "UTC"},
		"a day in capitals":          {[]string{"Mon 09:00-17:00"}, nil, "UTC"},
		"no minutes":                 {[]string{"mon 9-17"}, nil, "UTC"},
		"one digit hour":             {[]string{"mon 9:00-17:00"}, nil, "UTC"},
		"the end before the start":   {[]string{"mon 17:00-09:00"}, nil, "UTC"},
		"an empty interval":          {[]string{"mon 09:00-09:00"}, nil, "UTC"},
		"past midnight":              {[]string{"mon 09:00-25:00"}, nil, "UTC"},
		"24:30":                      {[]string{"mon 09:00-24:30"}, nil, "UTC"},
		"sixty minutes":              {[]string{"mon 09:60-17:00"}, nil, "UTC"},
		"something after":            {[]string{"mon 09:00-17:00 UTC"}, nil, "UTC"},
		"an empty line":              {[]string{"mon 09:00-17:00", ""}, nil, "UTC"},
		"overlapping intervals":      {[]string{"mon 09:00-12:00", "mon 11:00-13:00"}, nil, "UTC"},
		"the same interval twice":    {[]string{"mon 09:00-12:00", "mon 09:00-12:00"}, nil, "UTC"},
		"an unknown zone":            {officeWeek, nil, "Mars/Olympus_Mons"},
		"no zone":                    {officeWeek, nil, ""},
		"the machine's zone":         {officeWeek, nil, "Local"},
		"an offset, not a zone":      {officeWeek, nil, "+01:00"},
		"a holiday that is no date":  {officeWeek, []string{"25/12/2026"}, "UTC"},
		"a holiday with a time":      {officeWeek, []string{"2026-12-25T00:00:00Z"}, "UTC"},
		"a day that does not exist":  {officeWeek, []string{"2027-02-30"}, "UTC"},
		"more intervals than a week": {week22, nil, "UTC"},
		"more holidays than a year":  {officeWeek, many(worktime.MaxHolidays + 1), "UTC"},
	} {
		if _, err := worktime.New(c.week, c.holidays, c.zone); !errors.Is(err, worktime.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	// What is allowed at the edges.
	cal(t, []string{"sun 00:00-24:00"}, nil, "UTC")
	cal(t, officeWeek, many(worktime.MaxHolidays), "Europe/Paris")
	cal(t, officeWeek, []string{"2026-12-25", "2026-12-25"}, "UTC") // a holiday twice is one holiday
	cal(t, week22[:worktime.MaxIntervals], nil, "UTC")
}

func TestAfterAndBetween_RefuseWhatHasNoAnswer(t *testing.T) {
	c := cal(t, officeWeek, nil, "Europe/Paris")
	mon := utc(t, "2026-11-23 09:00")
	for name, d := range map[string]time.Duration{"zero": 0, "negative": -time.Second} {
		if _, err := c.After(mon, d); !errors.Is(err, worktime.ErrRange) {
			t.Errorf("After by a %s duration: %v, want ErrRange", name, err)
		}
	}
	if _, err := c.Between(mon, mon.Add(-time.Second)); !errors.Is(err, worktime.ErrRange) {
		t.Errorf("Between with the end before the start: %v, want ErrRange", err)
	}
	// One hour a week: 300 hours of it are more than five years away.
	rare := cal(t, []string{"mon 09:00-10:00"}, nil, "UTC")
	if _, err := rare.After(mon, 300*time.Hour); !errors.Is(err, worktime.ErrRange) {
		t.Errorf("After past the horizon: %v, want ErrRange", err)
	}
	if got, err := rare.After(mon, 100*time.Hour); err != nil || !got.Equal(utc(t, "2028-10-16 10:00")) {
		t.Errorf("100 hours at one a week = %v, %v; want Mon 2028-10-16 10:00", got, err)
	}
}

// The zone database is compiled into every binary that computes working
// time or shows a local time: the images are distroless static, with no
// /usr/share/zoneinfo. (`go list -deps` names every package a binary links.)
func TestTheZoneDatabaseIsCompiledIn(t *testing.T) {
	for _, cmd := range []string{"itemservice", "worker", "pli"} {
		out, err := exec.Command("go", "list", "-deps", "../../cmd/"+cmd).Output()
		if err != nil {
			t.Fatalf("go list -deps cmd/%s: %v", cmd, err)
		}
		if !strings.Contains("\n"+string(out), "\ntime/tzdata\n") {
			t.Errorf("cmd/%s does not link time/tzdata: an IANA zone would not load in its image", cmd)
		}
	}
}
