// Tests for Register (Ventoux item 5): the Item workflow and every activity
// it calls by name are registered by one function, used by NewWorker and by
// the story harness (internal/story), so the two cannot drift.
package workflow_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// recorder is a worker.Registry that keeps the names it is given. The
// embedded Registry is nil: a method this test does not define (Nexus)
// panics if Register calls it.
type recorder struct {
	worker.Registry
	workflows, activities []string
	unnamed               int // registrations without a name
}

func (r *recorder) RegisterWorkflow(w any) { r.unnamed++ }
func (r *recorder) RegisterWorkflowWithOptions(w any, o workflow.RegisterOptions) {
	r.workflows = append(r.workflows, o.Name)
}
func (r *recorder) RegisterDynamicWorkflow(w any, o workflow.DynamicRegisterOptions) { r.unnamed++ }
func (r *recorder) RegisterActivity(a any)                                           { r.unnamed++ }
func (r *recorder) RegisterActivityWithOptions(a any, o activity.RegisterOptions) {
	r.activities = append(r.activities, o.Name)
}
func (r *recorder) RegisterDynamicActivity(a any, o activity.DynamicRegisterOptions) { r.unnamed++ }

// activityNames reads the workflow's activity names from its source: every
// constant named Activity*, with its value.
func activityNames(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "item.go", nil, 0)
	if err != nil {
		t.Fatalf("parse item.go: %v", err)
	}
	var names []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, id := range vs.Names {
				if !strings.HasPrefix(id.Name, "Activity") || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: %v", id.Name, err)
				}
				names = append(names, v)
			}
		}
	}
	if len(names) < 15 {
		t.Fatalf("found %d activity names in item.go; the test is not reading what it should", len(names))
	}
	slices.Sort(names)
	return names
}

// Register registers the Item workflow by its name and every activity the
// workflow calls, each by its name, once, and nothing else.
func TestRegister_RegistersTheWorkflowAndEveryActivity(t *testing.T) {
	r := &recorder{}
	wf.Register(r, itemservice.New(pool), wf.WorkerOptions{})
	if want := []string{wf.Name}; !reflect.DeepEqual(r.workflows, want) {
		t.Errorf("workflows = %v, want %v", r.workflows, want)
	}
	got := slices.Clone(r.activities)
	slices.Sort(got)
	if want := activityNames(t); !reflect.DeepEqual(got, want) {
		t.Errorf("activities = %v\nwant (the Activity* constants of item.go) %v", got, want)
	}
	if r.unnamed != 0 {
		t.Errorf("%d registrations without a name: the workflow calls everything by name", r.unnamed)
	}
}

// With a CloseDelay the registered workflow is another function (the
// delayed one), still under the workflow's name: a worker and the harness
// get the same with the same options.
func TestRegister_FollowsTheOptions(t *testing.T) {
	plain, delayed := &recorder{}, &recorder{}
	wf.Register(plain, itemservice.New(pool), wf.WorkerOptions{})
	wf.Register(delayed, itemservice.New(pool), wf.WorkerOptions{CloseDelay: 1})
	if !reflect.DeepEqual(plain.workflows, delayed.workflows) || len(plain.workflows) != 1 {
		t.Errorf("workflows: %v and %v, want one, under one name", plain.workflows, delayed.workflows)
	}
	if !reflect.DeepEqual(plain.activities, delayed.activities) {
		t.Errorf("the options changed the activities: %v and %v", plain.activities, delayed.activities)
	}
}
