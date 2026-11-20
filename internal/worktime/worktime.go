// Package worktime is working-time arithmetic on a calendar: a week
// schedule, holidays and a time zone. It is pure: the same calendar and
// instants give the same answer on every machine, whatever its clock and
// its local zone (the zone database is compiled in).
//
// The kernel exposes it to Types as three CEL functions:
// calendar(week, holidays, zone), working_after(t, seconds, cal) and
// working_between(t1, t2, cal).
//
// PHASE B: declarations only. Every function refuses.
package worktime

import (
	"errors"
	"time"
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

// Calendar is a parsed calendar. The zero value is not usable; build one
// with New.
type Calendar struct{}

// New builds a calendar.
//   - week: lines such as "mon 09:00-17:00", in the calendar's local time;
//     several per day are allowed, "00:00-24:00" is a whole day; order
//     does not matter.
//   - holidays: ISO dates ("2026-12-25"); the whole local day is off.
//   - zone: an IANA name ("Europe/Paris").
func New(week, holidays []string, zone string) (*Calendar, error) {
	return nil, errors.New("worktime: not built yet")
}

// After returns the instant at which d of working time has passed since t.
// When t is outside working time, counting starts at the next opening. d
// must be positive.
func (c *Calendar) After(t time.Time, d time.Duration) (time.Time, error) {
	return time.Time{}, errors.New("worktime: not built yet")
}

// Between returns the working time between t1 and t2. t2 before t1 is
// ErrRange.
func (c *Calendar) Between(t1, t2 time.Time) (time.Duration, error) {
	return 0, errors.New("worktime: not built yet")
}
