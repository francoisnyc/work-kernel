package story

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Renderer writes events and side effects as timeline lines, in the
// grammar of the package documentation.
type Renderer struct {
	Day1    time.Time
	People  map[string]string    // principal -> the person's name
	Items   map[uuid.UUID]string // Item -> its name
	Hidden  map[string][]string  // Type ID -> fields whose values are left out
	Catalog *contracts.Catalog   // a call's inputs and outputs take their types from it
}

// Event renders one event of an Item of the Type ty (whose fields say how
// each value is written). own lists the fields the step that wrote it sent
// itself, left out of its values.
func (r Renderer) Event(ty *typedef.Type, ev itemservice.Event, own []string) Line {
	name, ok := r.Items[ev.ItemID]
	if !ok {
		name = ty.ID + "#?"
	}
	who := r.who(ev.Actor, ev.OnBehalfOf)
	vals := r.values(ty, ev.Values, ev.Added, own)
	with := func(s string) string {
		if vals != "" {
			return s + ": " + vals
		}
		return s
	}
	var text string
	switch ev.Op {
	case "create":
		s := who + " created it in " + ev.To
		if ev.Container != nil {
			s += " inside " + r.ref(ev.Container)
		}
		text = with(s)
	case "transition":
		text = with(fmt.Sprintf("%s moved it %s -> %s", who, ev.From, ev.To))
		if ev.LinkType != "" && ev.LinkTarget != nil {
			text += fmt.Sprintf("; linked %s to %s", ev.LinkType, r.ref(ev.LinkTarget))
		}
	case lifecycle.OpMigrated:
		text = fmt.Sprintf("%s migrated it %s@%d -> %s@%d", who, ev.TypeID, ev.PreviousVersion, ev.TypeID, ev.TypeVersion)
	case lifecycle.OpTransitionRequested:
		text = with(fmt.Sprintf("%s asked %s -> %s", who, ev.From, ev.RequestedTo))
	case lifecycle.OpTransitionDenied:
		text = fmt.Sprintf("%s denied %s -> %s: %s", who, ev.From, ev.RequestedTo, ev.Reason)
	case lifecycle.OpTransitionFailed:
		text = fmt.Sprintf("%s failed %s -> %s: %s", who, ev.From, ev.RequestedTo, ev.Reason)
	case lifecycle.OpLinkAdded:
		text = fmt.Sprintf("%s linked %s to %s", who, ev.LinkType, r.ref(ev.LinkTarget))
	case lifecycle.OpLinkRemoved:
		text = fmt.Sprintf("%s unlinked %s from %s", who, ev.LinkType, r.ref(ev.LinkTarget))
	case lifecycle.OpHookRan:
		text = who + " ran" + timerRun(ev.Timer, ev.Run)
	case lifecycle.OpHookFailed:
		text = fmt.Sprintf("%s failed%s: %s", who, timerRun(ev.Timer, ev.Run), ev.Reason)
	case lifecycle.OpReactionApplied:
		text = who + " applied to " + r.ref(ev.ReactionTarget)
	case lifecycle.OpReactionFailed:
		text = fmt.Sprintf("%s failed on %s: %s", who, r.ref(ev.ReactionTarget), ev.Reason)
	case lifecycle.OpReactionSkipped:
		text = who + " skipped: " + ev.Reason
	default: // update, and what an update can carry
		switch {
		case ev.Moved:
			text = with(fmt.Sprintf("%s moved it from %s to %s", who, r.place(ev.ContainerFrom), r.place(ev.ContainerTo)))
		case ev.Inherit:
			text = with(who + " gave it its container's readers")
		case len(ev.Readers) > 0:
			readers := make([]string, len(ev.Readers))
			for i, p := range ev.Readers {
				readers[i] = r.person(p)
			}
			text = with(who + " narrowed it to " + strings.Join(readers, ", "))
		default:
			s := who + " updated it"
			if ev.Timer != "" {
				s += " (" + ev.Timer + ")"
			}
			text = with(s)
		}
	}
	return Line{At: ev.CreatedAt, Item: name, Text: text}
}

// Call renders a side effect a fake was asked for.
func (r Renderer) Call(c Call) Line {
	action, _, _ := strings.Cut(c.Action, "@")
	connector, operation, _ := strings.Cut(action, ".")
	var ins, outs []contracts.Value
	if r.Catalog != nil {
		if contract, ok := r.Catalog.Lookup(c.Action); ok {
			ins, outs = contract.InputValues(), contract.OutputValues()
		}
	}
	item, ok := r.Items[c.ItemID]
	if !ok {
		item = "(unnamed)"
	}
	text := fmt.Sprintf("%s for %s (%s)", operation, item, c.Hook)
	if s := r.contractValues(c.Inputs, ins); s != "" {
		text += ": " + s
	}
	if s := r.contractValues(c.Outputs, outs); s != "" {
		text += " -> " + s
	}
	return Line{At: c.At, Item: connector, Text: text}
}

func timerRun(timer string, run int) string {
	switch {
	case timer == "":
		return ""
	case run > 0:
		return fmt.Sprintf(" (%s run %d)", timer, run)
	}
	return " (" + timer + ")"
}

// who is an event's actor, and whom it acted for.
func (r Renderer) who(actor, onBehalfOf string) string {
	var who string
	switch {
	case actor == "system:timer":
		who = "timer"
	case strings.HasPrefix(actor, "hook:"), strings.HasPrefix(actor, "reaction:"):
		who = actor[strings.LastIndex(actor, ":")+1:]
	default:
		who = r.person(actor)
	}
	if onBehalfOf != "" && onBehalfOf != actor {
		who += " for " + r.person(onBehalfOf)
	}
	return who
}

func (r Renderer) person(principal string) string {
	if name, ok := r.People[principal]; ok {
		return name
	}
	return principal
}

func (r Renderer) ref(ref *itemservice.ItemRef) string {
	switch {
	case ref == nil:
		return "nothing"
	case ref.Restricted:
		return "(hidden)"
	}
	if name, ok := r.Items[ref.ID]; ok {
		return name
	}
	return "(unnamed)"
}

func (r Renderer) place(ref *itemservice.ItemRef) string {
	if ref == nil {
		return "the top level"
	}
	return r.ref(ref)
}

// values writes field = value, sorted, leaving out own; an addition as
// field +N = value.
func (r Renderer) values(ty *typedef.Type, vals, added map[string]any, own []string) string {
	var parts []string
	for _, id := range sortedKeys(vals) {
		if slices.Contains(own, id) {
			continue
		}
		var sl slot
		if f, ok := ty.Field(id); ok {
			sl = fieldSlot(f)
		}
		v := r.value(vals[id], sl)
		if vals[id] != nil && slices.Contains(r.Hidden[ty.ID], id) {
			v = "..."
		}
		if d, ok := added[id]; ok {
			n := number(d)
			sign := "+"
			if strings.HasPrefix(n, "-") {
				sign, n = "-", n[1:]
			}
			parts = append(parts, fmt.Sprintf("%s %s%s = %s", id, sign, n, v))
			continue
		}
		parts = append(parts, id+" = "+v)
	}
	return strings.Join(parts, ", ")
}

func (r Renderer) contractValues(vals map[string]any, decl []contracts.Value) string {
	var parts []string
	for _, name := range sortedKeys(vals) {
		var sl slot
		if i := slices.IndexFunc(decl, func(v contracts.Value) bool { return v.Name == name }); i >= 0 {
			sl = contractSlot(decl[i])
		}
		parts = append(parts, name+" = "+r.value(vals[name], sl))
	}
	return strings.Join(parts, ", ")
}

// value writes one value as a story does, by its slot's type; with no
// slot, text is quoted and numbers plain.
func (r Renderer) value(v any, sl slot) string {
	if v == nil {
		return "null"
	}
	if list, ok := v.([]any); ok {
		one := sl
		one.multiple = false
		parts := make([]string, len(list))
		for i, e := range list {
			parts[i] = r.value(e, one)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case float64, int, int64, int32:
		return number(x)
	case string:
		switch sl.typ {
		case contracts.TypeEnum:
			return x
		case contracts.TypePrincipal:
			return r.person(x)
		case contracts.TypeDate:
			if d, ok := typedef.ParseDate(x); ok {
				return fmt.Sprintf("day %d", int(d.Sub(r.Day1).Hours()/24)+1)
			}
		case contracts.TypeDateTime:
			if t, ok := typedef.ParseDateTime(x); ok {
				return FormatTime(r.Day1, t)
			}
		}
		return strconv.Quote(x)
	}
	return fmt.Sprint(v)
}

func number(v any) string {
	switch x := v.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	}
	return fmt.Sprint(v)
}
