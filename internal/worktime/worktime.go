// Package worktime is working-time arithmetic on a calendar: a week
// schedule, holidays and a time zone. It is pure: the same calendar and
// instants give the same answer on every machine, whatever its clock and
// its local zone (the zone database is compiled in, below).
//
// The kernel exposes it to Types as three CEL functions:
// calendar(week, holidays, zone), working_after(t, seconds, cal) and
// working_between(t1, t2, cal).
package worktime

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	// The IANA zone database, compiled into every binary that links this
	// package: the images are distroless static, with no /usr/share/zoneinfo.
	// A blank import (the underscore) runs the package's init and nothing
	// else, like loading a JDBC driver class in Java.
	_ "time/tzdata"
)

// ErrInvalid: the calendar is not one (a line of the week that does not
// parse, intervals that overlap, an unknown zone, no working time at all).
var ErrInvalid = errors.New("worktime: invalid calendar")

// ErrRange: the question has no answer (a duration that is not positive,
// an end before its start, an answer further away than Horizon).
var ErrRange = errors.New("worktime: out of range")

// Horizon is how far ahead After looks before giving up.
const Horizon = 5 * 366 * 24 * time.Hour

// MaxIntervals and MaxHolidays bound a calendar.
const (
	MaxIntervals = 21
	MaxHolidays  = 366
)

// span is one working interval of a day, in minutes from local midnight.
type span struct{ from, to int }

// Calendar is a parsed calendar. The zero value is not usable; build one
// with New.
type Calendar struct {
	loc      *time.Location
	week     [7][]span       // by time.Weekday; sorted, touching spans merged
	holidays map[string]bool // local dates, YYYY-MM-DD
}

var weekLine = regexp.MustCompile(`^(mon|tue|wed|thu|fri|sat|sun) (\d\d):(\d\d)-(\d\d):(\d\d)$`)

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

func invalid(format string, args ...any) error {
	// %w wraps ErrInvalid, so errors.Is(err, ErrInvalid) holds: the Go
	// way to say "this is one of those", where Java would subclass.
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// minuteOfDay reads "HH" and "MM" as minutes from midnight; 24:00 is the
// end of the day.
func minuteOfDay(hh, mm string) (int, bool) {
	h, _ := strconv.Atoi(hh)
	m, _ := strconv.Atoi(mm)
	if m > 59 || h > 24 || (h == 24 && m != 0) {
		return 0, false
	}
	return h*60 + m, true
}

// New builds a calendar.
//   - week: lines such as "mon 09:00-17:00", in the calendar's local time;
//     several per day are allowed, "00:00-24:00" is a whole day; order
//     does not matter.
//   - holidays: ISO dates ("2026-12-25"); the whole local day is off.
//   - zone: an IANA name ("Europe/Paris").
func New(week, holidays []string, zone string) (*Calendar, error) {
	if len(week) == 0 {
		return nil, invalid("no working time")
	}
	if len(week) > MaxIntervals {
		return nil, invalid("%d intervals; at most %d", len(week), MaxIntervals)
	}
	// "Local" would be this machine's zone: never the same answer everywhere.
	if zone == "" || zone == "Local" {
		return nil, invalid("the zone must be an IANA name")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, invalid("unknown zone")
	}
	c := &Calendar{loc: loc, holidays: map[string]bool{}}
	for _, line := range week {
		m := weekLine.FindStringSubmatch(line)
		if m == nil {
			return nil, invalid("a line of the week is not like \"mon 09:00-17:00\"")
		}
		from, ok1 := minuteOfDay(m[2], m[3])
		to, ok2 := minuteOfDay(m[4], m[5])
		if !ok1 || !ok2 || from >= to {
			return nil, invalid("an interval of the week does not end after it starts, within one day")
		}
		d := weekdays[m[1]]
		c.week[d] = append(c.week[d], span{from, to})
	}
	for d := range c.week {
		spans := c.week[d]
		slices.SortFunc(spans, func(a, b span) int { return a.from - b.from })
		var merged []span
		for _, s := range spans {
			if n := len(merged); n > 0 {
				switch last := &merged[n-1]; {
				case s.from < last.to:
					return nil, invalid("two intervals of one day overlap")
				case s.from == last.to: // touching: one interval
					last.to = s.to
					continue
				}
			}
			merged = append(merged, s)
		}
		c.week[d] = merged
	}
	for _, h := range holidays {
		// time.Parse is strict about the layout: a time of day, or a day
		// that does not exist, is an error.
		if _, err := time.Parse(time.DateOnly, h); err != nil {
			return nil, invalid("a holiday is not a date like 2026-12-25")
		}
		c.holidays[h] = true
	}
	if len(c.holidays) > MaxHolidays {
		return nil, invalid("%d holidays; at most %d", len(c.holidays), MaxHolidays)
	}
	return c, nil
}

// day is a local calendar date.
type day struct {
	y int
	m time.Month
	d int
}

func (c *Calendar) dayOf(t time.Time) day {
	y, m, d := t.In(c.loc).Date()
	return day{y, m, d}
}

// noon is the date's noon in UTC: a handle for date arithmetic and for
// the weekday, which no zone change can move to another date.
func (d day) noon() time.Time { return time.Date(d.y, d.m, d.d, 12, 0, 0, 0, time.UTC) }

func (d day) next() day {
	y, m, dd := d.noon().AddDate(0, 0, 1).Date()
	return day{y, m, dd}
}

func (c *Calendar) offsetAt(t time.Time) int {
	_, off := t.In(c.loc).Zone()
	return off
}

// instant turns a local wall-clock time (a date and minutes from its
// midnight) into an instant. Around a zone change a wall-clock time may not
// exist, or exist twice:
//   - it does not exist (clocks jump forward over it): the instant of the
//     jump, so an interval loses the skipped time;
//   - it exists twice (clocks are set back): the first for an interval's
//     start, the last for its end (late), so the repeated time is worked
//     both times.
func (c *Calendar) instant(d day, minute int, late bool) time.Time {
	// The wall clock read as if it were UTC, then shifted by the zone's
	// offset before and after any change nearby.
	wall := time.Date(d.y, d.m, d.d, 0, minute, 0, 0, time.UTC)
	before, after := c.offsetAt(wall.Add(-24*time.Hour)), c.offsetAt(wall.Add(24*time.Hour))
	c1 := wall.Add(-time.Duration(before) * time.Second)
	c2 := wall.Add(-time.Duration(after) * time.Second)
	ok1, ok2 := c.offsetAt(c1) == before, c.offsetAt(c2) == after
	switch {
	case ok1 && ok2:
		if c1.After(c2) == late {
			return c1
		}
		return c2
	case ok1:
		return c1
	case ok2:
		return c2
	}
	// Neither: the time was skipped. Find the instant of the change
	// between the two candidates (c2 is before it, c1 after).
	lo, hi := c2.Unix(), c1.Unix()
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if c.offsetAt(time.Unix(mid, 0)) == before {
			lo = mid
		} else {
			hi = mid
		}
	}
	return time.Unix(hi, 0).UTC()
}

// spans calls f with each working interval of the days from the one that
// holds `from`, in order, as instants, until f returns false or a day
// starts after `until`.
func (c *Calendar) spans(from, until time.Time, f func(start, end time.Time) bool) {
	var prevEnd time.Time
	for d := c.dayOf(from); ; d = d.next() {
		if c.instant(d, 0, false).After(until) {
			return
		}
		if c.holidays[d.noon().Format(time.DateOnly)] {
			continue
		}
		for _, s := range c.week[d.noon().Weekday()] {
			start, end := c.instant(d, s.from, false), time.Time{}
			if s.to == 24*60 {
				end = c.instant(d.next(), 0, true)
			} else {
				end = c.instant(d, s.to, true)
			}
			// Where clocks were set back, two intervals of one day can
			// meet in a repeated hour: never count an instant twice.
			if start.Before(prevEnd) {
				start = prevEnd
			}
			if !end.After(start) {
				continue
			}
			prevEnd = end
			if !f(start, end) {
				return
			}
		}
	}
}

// After returns the instant at which d of working time has passed since t.
// When t is outside working time, counting starts at the next opening. d
// must be positive.
func (c *Calendar) After(t time.Time, d time.Duration) (time.Time, error) {
	if d <= 0 {
		return time.Time{}, fmt.Errorf("%w: the working time to count must be positive", ErrRange)
	}
	var out time.Time
	left := d
	c.spans(t, t.Add(Horizon), func(start, end time.Time) bool {
		if !end.After(t) {
			return true
		}
		if start.Before(t) {
			start = t
		}
		if room := end.Sub(start); room < left {
			left -= room
			return true
		}
		out = start.Add(left).UTC()
		return false
	})
	if out.IsZero() {
		return time.Time{}, fmt.Errorf("%w: further away than the calendar looks ahead", ErrRange)
	}
	return out, nil
}

// Between returns the working time between t1 and t2. t2 before t1 is
// ErrRange.
func (c *Calendar) Between(t1, t2 time.Time) (time.Duration, error) {
	if t2.Before(t1) {
		return 0, fmt.Errorf("%w: the end is before the start", ErrRange)
	}
	var total time.Duration
	c.spans(t1, t2, func(start, end time.Time) bool {
		if start.Before(t1) {
			start = t1
		}
		if end.After(t2) {
			end = t2
		}
		if end.After(start) {
			total += end.Sub(start)
		}
		return true
	})
	return total, nil
}
