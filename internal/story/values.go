package story

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// slot is what a value is given to: a field, or an action's input or
// output. Its type is one of typedef's field types (the contracts name
// theirs the same).
type slot struct {
	name     string
	typ      string
	options  []string // enum
	multiple bool
	nullable bool
}

func fieldSlot(f typedef.Field) slot {
	return slot{name: f.ID, typ: string(f.Type), options: f.Options, multiple: f.Multiple, nullable: true}
}

func contractSlot(v contracts.Value) slot {
	return slot{name: v.Name, typ: v.Type, options: v.Options, nullable: v.Nullable}
}

// converter turns a story's values into the kernel's: dates and instants
// in the kernel's forms, people as principals.
type converter struct {
	day1   time.Time
	people map[string]string // name -> principal
}

func newConverter(s *Story) converter {
	c := converter{day1: s.Day1, people: map[string]string{}}
	for _, p := range s.People {
		c.people[p.Name] = p.Principal()
	}
	if _, ok := c.people[DefaultObserver]; !ok && s.Observer == "" {
		c.people[DefaultObserver] = "user:" + DefaultObserver
	}
	return c
}

// value converts v for sl, or says why it does not fit.
func (c converter) value(v Value, sl slot) (any, error) {
	if v.Kind == Null {
		if !sl.nullable {
			return nil, fmt.Errorf("%s may not be null", sl.name)
		}
		return nil, nil
	}
	if sl.multiple {
		if v.Kind != List {
			return nil, fmt.Errorf("%s holds several values: write them as [a, b]", sl.name)
		}
		one := sl
		one.multiple, one.nullable = false, false
		out := make([]any, 0, len(v.List))
		for _, e := range v.List {
			x, err := c.value(e, one)
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, nil
	}
	bad := func(want string) error {
		return fmt.Errorf("%s = %s: want %s", sl.name, v.written(), want)
	}
	switch sl.typ {
	case contracts.TypeInt:
		if v.Kind != Int {
			return nil, bad("a whole number")
		}
		return v.Int, nil
	case contracts.TypeBool:
		if v.Kind != Bool {
			return nil, bad("true or false")
		}
		return v.Bool, nil
	case contracts.TypeString:
		if v.Kind != Quoted && v.Kind != Word {
			return nil, bad("text")
		}
		return v.Text, nil
	case contracts.TypeEnum:
		if (v.Kind != Word && v.Kind != Quoted) || !slices.Contains(sl.options, v.Text) {
			return nil, bad("one of " + strings.Join(sl.options, ", "))
		}
		return v.Text, nil
	case contracts.TypePrincipal:
		if v.Kind != Word && v.Kind != Quoted {
			return nil, bad("one of the story's people")
		}
		if p, ok := c.people[v.Text]; ok {
			return p, nil
		}
		if _, ok := item.KindOf(v.Text); ok && strings.Contains(v.Text, ":") {
			return v.Text, nil
		}
		return nil, fmt.Errorf("%s = %s: %s is not one of the story's people", sl.name, v.Text, v.Text)
	case contracts.TypeDate:
		if v.Kind != Day {
			return nil, bad("a date, such as day 15")
		}
		return c.day1.AddDate(0, 0, v.Day-1).Format(time.DateOnly), nil
	case contracts.TypeDateTime:
		if v.Kind != DayTime {
			return nil, bad("an instant, such as day 3 14:30")
		}
		return c.day1.AddDate(0, 0, v.Day-1).Add(v.Time).UTC().Format(typedef.DateTimeLayout), nil
	}
	return nil, fmt.Errorf("%s: a value of type %q cannot be written in a story", sl.name, sl.typ)
}

// written is the value as a story writes it.
func (v Value) written() string {
	switch v.Kind {
	case Int:
		return fmt.Sprint(v.Int)
	case Bool:
		return fmt.Sprint(v.Bool)
	case Null:
		return "null"
	case Quoted:
		return fmt.Sprintf("%q", v.Text)
	case Day:
		return fmt.Sprintf("day %d", v.Day)
	case DayTime:
		return fmt.Sprintf("day %d %s", v.Day, clockOf(v.Time))
	case List:
		parts := make([]string, len(v.List))
		for i, e := range v.List {
			parts[i] = e.written()
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return v.Text
}

func clockOf(d time.Duration) string {
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if s != 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", h, m)
}
