package reading_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// exprType is a Type with a field of every kind and one move, from a to b,
// guarded by expr.
func exprType(t *testing.T, expr string) *typedef.Type {
	t.Helper()
	src := fmt.Sprintf(`id: expr
version: 1
initial_state: a
fields:
  - {id: f_a, type: int}
  - {id: f_b, type: int}
  - {id: f_d, type: date}
  - {id: f_e, type: date}
  - {id: f_t, type: datetime}
  - {id: f_u, type: datetime}
  - {id: f_x, type: string}
  - {id: f_k, type: enum, options: [x, y, z]}
  - {id: f_flag, type: bool}
  - {id: f_set, type: string, multiple: true}
states:
  - {id: a, category: todo}
  - {id: b, category: done}
transitions:
  - {from: a, to: b, guard: %q}
`, expr)
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("the Type for %s: %v", expr, err)
	}
	return ty
}

// holds evaluates the guard as the item service does at run time: through
// lifecycle.TransitionWith, the one function its Transition
// (internal/itemservice/transition.go) and a held move's resolution
// (holds.go) call. The move happens exactly when the guard holds.
func holds(ty *typedef.Type, fields map[string]any, today time.Time) bool {
	_, err := lifecycle.TransitionWith(ty, "a", lifecycle.Input{Fields: fields, Today: today}, "b")
	return err == nil
}

// The vocabulary of conditions, each phrase held to the kernel's own
// evaluation on its boundary cases: the words say what the engine does.
// Anything outside the closed subset is "a rule over ... holds", naming
// what it reads, never a guess at its meaning.
func TestCondition_WordsMatchTheEngine(t *testing.T) {
	today := time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC)
	type eval struct {
		fields map[string]any
		want   bool
	}
	f := func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	cases := []struct {
		expr  string
		words string
		evals []eval
	}{
		// Numbers.
		{"f_a >= 0", "`f_a` is at least 0", []eval{{f("f_a", 0), true}, {f("f_a", -1), false}, {f(), false}}},
		{"f_a > 1", "`f_a` is more than 1", []eval{{f("f_a", 1), false}, {f("f_a", 2), true}}},
		{"f_a < 5", "`f_a` is less than 5", []eval{{f("f_a", 5), false}, {f("f_a", 4), true}}},
		{"f_a <= 5", "`f_a` is at most 5", []eval{{f("f_a", 5), true}, {f("f_a", 6), false}}},
		{"f_a == 3", "`f_a` is 3", []eval{{f("f_a", 3), true}, {f("f_a", 4), false}}},
		{"f_a != 3", "`f_a` is not 3", []eval{{f("f_a", 3), false}, {f("f_a", 4), true}}},
		{"f_a <= f_b", "`f_a` is at most `f_b`", []eval{{f("f_a", 2, "f_b", 2), true}, {f("f_a", 3, "f_b", 2), false}}},
		{"f_a + f_b > 10", "`f_a` plus `f_b` is more than 10", []eval{{f("f_a", 5, "f_b", 6), true}, {f("f_a", 5, "f_b", 5), false}}},
		{"f_a - f_b >= 1", "`f_a` minus `f_b` is at least 1", []eval{{f("f_a", 5, "f_b", 4), true}, {f("f_a", 5, "f_b", 5), false}}},
		// Dates and instants.
		{"f_d >= today", "`f_d` is on or after today", []eval{{f("f_d", "2026-12-07"), true}, {f("f_d", "2026-12-06"), false}}},
		{"f_d > today", "`f_d` is after today", []eval{{f("f_d", "2026-12-07"), false}, {f("f_d", "2026-12-08"), true}}},
		{"f_d < f_e", "`f_d` is before `f_e`", []eval{{f("f_d", "2026-12-07", "f_e", "2026-12-07"), false},
			{f("f_d", "2026-12-06", "f_e", "2026-12-07"), true}}},
		{"f_d <= f_e", "`f_d` is on or before `f_e`", []eval{{f("f_d", "2026-12-07", "f_e", "2026-12-07"), true},
			{f("f_d", "2026-12-08", "f_e", "2026-12-07"), false}}},
		{"f_t > f_u", "`f_t` is after `f_u`", []eval{{f("f_t", "2026-12-07T10:00:00.000000Z", "f_u", "2026-12-07T10:00:00.000000Z"), false},
			{f("f_t", "2026-12-07T10:00:01.000000Z", "f_u", "2026-12-07T10:00:00.000000Z"), true}}},
		// Set or unset.
		{"f_x == null", "`f_x` is unset", []eval{{f(), true}, {f("f_x", "a"), false}}},
		{"f_x != null", "`f_x` is set", []eval{{f(), false}, {f("f_x", "a"), true}}},
		// Text, options, yes or no.
		{`f_k == "x"`, "`f_k` is x", []eval{{f("f_k", "x"), true}, {f("f_k", "y"), false}}},
		{`f_x == "two words"`, "`f_x` is \"two words\"", []eval{{f("f_x", "two words"), true}, {f("f_x", "two"), false}}},
		{`f_k in ["x", "y"]`, "`f_k` is x or y", []eval{{f("f_k", "y"), true}, {f("f_k", "z"), false}}},
		{"f_flag == true", "`f_flag` is true", []eval{{f("f_flag", true), true}, {f("f_flag", false), false}}},
		// And, or: one kind in a chain.
		{"f_a == null || f_a >= 0", "`f_a` is unset or `f_a` is at least 0",
			[]eval{{f(), true}, {f("f_a", 0), true}, {f("f_a", -1), false}}},
		{"f_a > 0 && f_b > 0 && f_a < f_b", "`f_a` is more than 0, `f_b` is more than 0 and `f_a` is less than `f_b`",
			[]eval{{f("f_a", 1, "f_b", 2), true}, {f("f_a", 2, "f_b", 2), false}, {f("f_a", 0, "f_b", 2), false}}},
		// Outside the subset: what it reads, never a guess.
		{"f_a > 0 && (f_b == null || f_b > 0)", "a rule over `f_a` and `f_b` holds",
			[]eval{{f("f_a", 1), true}, {f("f_a", 1, "f_b", 0), false}}},
		{"!(f_a > 0)", "a rule over `f_a` holds", []eval{{f("f_a", 0), true}, {f("f_a", 1), false}}},
		{"size(f_set) > 1", "a rule over `f_set` holds", []eval{{f("f_set", []any{"a", "b"}), true}, {f("f_set", []any{"a"}), false}}},
		{`f_a > 2 ? f_k == "x" : f_k == "y"`, "a rule over `f_a` and `f_k` holds",
			[]eval{{f("f_a", 3, "f_k", "x"), true}, {f("f_a", 1, "f_k", "x"), false}}},
		{"f_d >= today && f_a > 0 || f_flag == true", "a rule over `f_a`, `f_d`, `f_flag` and today holds",
			[]eval{{f("f_flag", true), true}, {f("f_a", 0, "f_d", "2026-12-07"), false}}},
	}
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			ty := exprType(t, c.expr)
			if got := reading.Condition(ty, c.expr); got != c.words {
				t.Errorf("Condition(%s) = %q, want %q", c.expr, got, c.words)
			}
			for _, e := range c.evals {
				if got := holds(ty, e.fields, today); got != e.want {
					t.Errorf("the engine: %s on %v = %v, want %v (the words: %s)", c.expr, e.fields, got, e.want, c.words)
				}
			}
		})
	}
}

// What a condition reads, outside the subset, is named in the reader's
// words: a container's field, links, the Items inside, today.
func TestCondition_WhatARuleReads(t *testing.T) {
	cases := map[string]string{
		`container.state == "pending"`:                                   "its container's state is pending",
		`children.approval.filter(c, c.state == "approved").size() >= 2`: "a rule over the approvals inside it holds",
		`links.issues.all(i, i.category in ["done", "canceled"])`:        "a rule over its `issues` links holds",
	}
	src := `id: around
version: 1
initial_state: a
states:
  - {id: a, category: todo}
  - {id: b, category: done}
links:
  - {name: issues, inverse: in_epic, targets: [around]}
transitions:
  - {from: a, to: b}
`
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for expr, want := range cases {
		if got := reading.Condition(ty, expr); got != want {
			t.Errorf("Condition(%s) = %q, want %q", expr, got, want)
		}
	}
	if !strings.Contains(reading.Condition(ty, "today > today"), "today") {
		t.Error("a condition on today does not say so")
	}
}
