package reading

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Expressions in words. A closed subset reads in words: fields and the
// kernel's variables, literals, plus and minus, comparisons, "is set" and
// "is unset", "is one of", and a chain of "and" or of "or" (never both: a
// reader cannot see the brackets). Anything else reads as what it reads:
// "a rule over `f_a` and today holds", "computed from `f_priority`". The
// words never guess at a meaning; a test holds each phrase to the
// engine's own evaluation (expr_test.go).

// exprCtx is what an expression is read against: the Type whose fields it
// names, and what cause.at means where it is used.
type exprCtx struct {
	ty      *typedef.Type
	causeAt string // "the time of the write"; "that instant" in an acting timer at an instant
}

func newCtx(ty *typedef.Type) exprCtx { return exprCtx{ty: ty, causeAt: "the time of the write"} }

// parser reads CEL's syntax only: no declarations are needed to read words.
var parser, _ = cel.NewEnv()

func parse(expr string) (celast.Expr, bool) {
	a, iss := parser.Parse(expr)
	if iss != nil && iss.Err() != nil {
		return nil, false
	}
	return a.NativeRep().Expr(), true
}

// Condition renders a CEL condition over the Type's fields (a guard, an
// invariant's rule, a reaction's when) in words: the closed subset in
// words ("`f_end` is on or after `f_start`"), anything else as "a rule
// over {what it reads} holds".
func Condition(t *typedef.Type, expr string) string {
	return newCtx(t).condition(expr)
}

func (c exprCtx) condition(expr string) string {
	e, ok := parse(expr)
	if !ok {
		return "a rule holds"
	}
	if words, ok := c.cond(e); ok {
		return words
	}
	return "a rule over " + joinAnd(c.refPhrases(e)) + " holds"
}

// value renders an expression that computes a value: the words, and
// whether they are in the subset; outside it, the phrases of what it reads.
func (c exprCtx) value(expr string) (string, bool, []string) {
	e, ok := parse(expr)
	if !ok {
		return "", false, nil
	}
	if e.Kind() == celast.LiteralKind && e.AsLiteral().Type() == types.NullType {
		return "nothing", true, nil
	}
	if words, ok := c.term(e); ok {
		return words, true, nil
	}
	return "", false, c.refPhrases(e)
}

// cond renders a condition in the subset.
func (c exprCtx) cond(e celast.Expr) (string, bool) {
	if e.Kind() != celast.CallKind {
		if e.Kind() == celast.LiteralKind && e.AsLiteral().Type() == types.BoolType {
			return fmt.Sprint(e.AsLiteral().Value()), true
		}
		return "", false
	}
	call := e.AsCall()
	args := call.Args()
	switch fn := call.FunctionName(); fn {
	case operators.LogicalAnd, operators.LogicalOr:
		var leaves []celast.Expr
		flatten(e, fn, &leaves)
		parts := make([]string, len(leaves))
		for i, l := range leaves {
			if l.Kind() == celast.CallKind {
				if f := l.AsCall().FunctionName(); f == operators.LogicalAnd || f == operators.LogicalOr {
					return "", false // and and or mixed: a reader cannot see the brackets
				}
			}
			w, ok := c.cond(l)
			if !ok {
				return "", false
			}
			parts[i] = w
		}
		if fn == operators.LogicalAnd {
			return joinAnd(parts), true
		}
		return joinOr(parts), true
	case operators.Equals, operators.NotEquals:
		if len(args) != 2 {
			return "", false
		}
		for i, other := range []int{1, 0} {
			if isNull(args[i]) {
				w, ok := c.term(args[other])
				if !ok {
					return "", false
				}
				if fn == operators.Equals {
					return w + " is unset", true
				}
				return w + " is set", true
			}
		}
		l, ok1 := c.term(args[0])
		r, ok2 := c.term(args[1])
		if !ok1 || !ok2 {
			return "", false
		}
		if fn == operators.Equals {
			return l + " is " + r, true
		}
		return l + " is not " + r, true
	case operators.Less, operators.LessEquals, operators.Greater, operators.GreaterEquals:
		if len(args) != 2 {
			return "", false
		}
		l, ok1 := c.term(args[0])
		r, ok2 := c.term(args[1])
		if !ok1 || !ok2 {
			return "", false
		}
		words := map[string][2]string{
			operators.Less:          {"less than", "before"},
			operators.LessEquals:    {"at most", "on or before"},
			operators.Greater:       {"more than", "after"},
			operators.GreaterEquals: {"at least", "on or after"},
		}[fn]
		w := words[0]
		if c.isTime(args[0]) || c.isTime(args[1]) {
			w = words[1]
		}
		return l + " is " + w + " " + r, true
	case operators.In:
		if len(args) != 2 || args[1].Kind() != celast.ListKind {
			return "", false
		}
		l, ok := c.term(args[0])
		if !ok {
			return "", false
		}
		var items []string
		for _, el := range args[1].AsList().Elements() {
			w, ok := c.term(el)
			if !ok {
				return "", false
			}
			items = append(items, w)
		}
		if len(items) == 0 {
			return "", false
		}
		return l + " is " + joinOr(items), true
	}
	return "", false
}

func flatten(e celast.Expr, fn string, out *[]celast.Expr) {
	if e.Kind() == celast.CallKind && e.AsCall().FunctionName() == fn {
		for _, a := range e.AsCall().Args() {
			flatten(a, fn, out)
		}
		return
	}
	*out = append(*out, e)
}

func isNull(e celast.Expr) bool {
	return e.Kind() == celast.LiteralKind && e.AsLiteral().Type() == types.NullType
}

var bareWord = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// term renders a value in the subset: a name, a literal, plus or minus.
func (c exprCtx) term(e celast.Expr) (string, bool) {
	switch e.Kind() {
	case celast.LiteralKind:
		v := e.AsLiteral()
		switch v.Type() {
		case types.StringType:
			s := v.Value().(string)
			if bareWord.MatchString(s) {
				return s, true
			}
			return fmt.Sprintf("%q", s), true
		case types.IntType, types.UintType, types.BoolType:
			return fmt.Sprint(v.Value()), true
		}
		return "", false
	case celast.IdentKind, celast.SelectKind:
		path, ok := selectPath(e)
		if !ok {
			return "", false
		}
		return c.nameWords(path)
	case celast.CallKind:
		call := e.AsCall()
		args := call.Args()
		switch call.FunctionName() {
		case operators.Add, operators.Subtract:
			if len(args) != 2 {
				return "", false
			}
			l, ok1 := c.term(args[0])
			r, ok2 := c.term(args[1])
			if !ok1 || !ok2 {
				return "", false
			}
			if call.FunctionName() == operators.Add {
				return l + " plus " + r, true
			}
			return l + " minus " + r, true
		case operators.Negate:
			if len(args) == 1 && args[0].Kind() == celast.LiteralKind {
				if w, ok := c.term(args[0]); ok {
					return "-" + w, true
				}
			}
		}
	}
	return "", false
}

// selectPath is a name and its selections, dotted: container.linked.calendar.f_week.
func selectPath(e celast.Expr) ([]string, bool) {
	switch e.Kind() {
	case celast.IdentKind:
		return []string{e.AsIdent()}, true
	case celast.SelectKind:
		s := e.AsSelect()
		if s.IsTestOnly() {
			return nil, false
		}
		head, ok := selectPath(s.Operand())
		if !ok {
			return nil, false
		}
		return append(head, s.FieldName()), true
	}
	return nil, false
}

// nameWords renders a name the subset reads.
func (c exprCtx) nameWords(path []string) (string, bool) {
	switch {
	case len(path) == 1 && path[0] == typedef.VarToday:
		return "today", true
	case len(path) == 1 && path[0] == typedef.EachVar:
		return "each value", true
	case len(path) == 2 && path[0] == "cause" && path[1] == "at":
		return c.causeAt, true
	case len(path) == 1:
		return "`" + path[0] + "`", true
	case len(path) == 2 && path[0] == "child":
		return "`" + path[1] + "`", true
	case len(path) == 2 && path[0] == typedef.ContainerVar && (path[1] == "state" || path[1] == "category"):
		return "its container's " + path[1], true
	case len(path) == 2 && path[0] == typedef.ContainerVar:
		return "its container's `" + path[1] + "`", true
	case len(path) == 4 && path[0] == typedef.ContainerVar && path[1] == "linked":
		return "`" + path[3] + "` of its container's `" + path[2] + "`", true
	case len(path) == 3 && path[0] == "linked":
		return "`" + path[2] + "` of its `" + path[1] + "`", true
	}
	return "", false
}

// isTime reports whether a term is a date or an instant: a date or
// datetime field, today, or cause.at.
func (c exprCtx) isTime(e celast.Expr) bool {
	path, ok := selectPath(e)
	if !ok {
		return false
	}
	switch {
	case len(path) == 1 && path[0] == typedef.VarToday:
		return true
	case len(path) == 2 && path[0] == "cause" && path[1] == "at":
		return true
	case len(path) == 1 && c.ty != nil:
		if f, ok := c.ty.Field(path[0]); ok {
			return f.Type == typedef.Date || f.Type == typedef.DateTime
		}
	}
	return false
}

// refPhrases are what an expression reads, in words: its own fields
// (sorted), then today, the time of the write and each value, then the
// rest, sorted. A comprehension's own variables are not reads.
func (c exprCtx) refPhrases(e celast.Expr) []string {
	seen := map[string]bool{}
	var fields, vars, others []string
	add := func(p string, list *[]string) {
		if !seen[p] {
			seen[p] = true
			*list = append(*list, p)
		}
	}
	var walk func(e celast.Expr, bound []string)
	walk = func(e celast.Expr, bound []string) {
		switch e.Kind() {
		case celast.IdentKind, celast.SelectKind:
			if path, ok := selectPath(e); ok {
				if slices.Contains(bound, path[0]) {
					return
				}
				phrase, kind := c.refPhrase(path)
				switch kind {
				case refField:
					add(phrase, &fields)
				case refVar:
					add(phrase, &vars)
				default:
					add(phrase, &others)
				}
				return
			}
			if e.Kind() == celast.SelectKind {
				walk(e.AsSelect().Operand(), bound)
			}
		case celast.CallKind:
			call := e.AsCall()
			if call.IsMemberFunction() {
				walk(call.Target(), bound)
			}
			for _, a := range call.Args() {
				walk(a, bound)
			}
		case celast.ListKind:
			for _, el := range e.AsList().Elements() {
				walk(el, bound)
			}
		case celast.ComprehensionKind:
			cp := e.AsComprehension()
			inner := append(slices.Clone(bound), cp.IterVar(), cp.AccuVar())
			if cp.HasIterVar2() {
				inner = append(inner, cp.IterVar2())
			}
			walk(cp.IterRange(), bound)
			walk(cp.AccuInit(), inner)
			walk(cp.LoopCondition(), inner)
			walk(cp.LoopStep(), inner)
			walk(cp.Result(), inner)
		}
	}
	walk(e, nil)
	sort.Strings(fields)
	order := map[string]int{"today": 0, c.causeAt: 1, "each value": 2}
	sort.SliceStable(vars, func(i, j int) bool { return order[vars[i]] < order[vars[j]] })
	sort.Strings(others)
	return append(append(fields, vars...), others...)
}

type refKind int

const (
	refField refKind = iota
	refVar
	refOther
)

// refPhrase is one read in words.
func (c exprCtx) refPhrase(path []string) (string, refKind) {
	switch {
	case path[0] == typedef.VarToday:
		return "today", refVar
	case path[0] == "cause":
		return c.causeAt, refVar
	case path[0] == typedef.EachVar:
		return "each value", refVar
	case path[0] == "child" && len(path) > 1:
		return "`" + path[1] + "`", refField
	case path[0] == typedef.ContainerVar && len(path) >= 3 && path[1] == "linked":
		return "its container's `" + path[2] + "`", refOther
	case path[0] == typedef.ContainerVar && len(path) >= 2 && (path[1] == "state" || path[1] == "category"):
		return "its container's state", refOther
	case path[0] == typedef.ContainerVar && len(path) >= 2:
		return "its container's `" + path[1] + "`", refOther
	case path[0] == typedef.ContainerVar:
		return "its container", refOther
	case path[0] == "linked" && len(path) >= 2:
		return "its `" + path[1] + "`", refOther
	case path[0] == "links" && len(path) >= 2:
		return "its `" + path[1] + "` links", refOther
	case path[0] == typedef.ChildrenVar && len(path) >= 2:
		return "the " + plural([]string{path[1]}) + " inside it", refOther
	}
	return "`" + path[0] + "`", refField
}

// joinPlain joins with commas, the last with word: "a, b and c".
func joinPlain(items []string, word string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + word + " " + items[len(items)-1]
}

func joinAnd(items []string) string { return joinPlain(items, "and") }
func joinOr(items []string) string  { return joinPlain(items, "or") }
