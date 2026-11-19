package api

import (
	"context"
	"errors"
	"maps"
	"slices"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Types are read-only (they load from files at start) and readable by anyone
// in the namespace. They are global for now, so every namespace sees the same
// list under its own names (docs/journal.md, Later: Types per namespace).

var fieldTypes = map[typedef.FieldType]kernelv1.FieldType{
	typedef.String:    kernelv1.FieldType_FIELD_TYPE_STRING,
	typedef.Int:       kernelv1.FieldType_FIELD_TYPE_INT,
	typedef.Bool:      kernelv1.FieldType_FIELD_TYPE_BOOL,
	typedef.Enum:      kernelv1.FieldType_FIELD_TYPE_ENUM,
	typedef.Principal: kernelv1.FieldType_FIELD_TYPE_PRINCIPAL,
	typedef.Date:      kernelv1.FieldType_FIELD_TYPE_DATE,
	typedef.DateTime:  kernelv1.FieldType_FIELD_TYPE_DATETIME,
}

var fieldWriters = map[typedef.WritableBy]kernelv1.FieldWriter{
	typedef.WritableByAnyone: kernelv1.FieldWriter_FIELD_WRITER_UNSPECIFIED,
	typedef.WritableByKernel: kernelv1.FieldWriter_FIELD_WRITER_KERNEL,
}

var categories = map[string]kernelv1.StateCategory{
	"triage":   kernelv1.StateCategory_STATE_CATEGORY_TRIAGE,
	"backlog":  kernelv1.StateCategory_STATE_CATEGORY_BACKLOG,
	"todo":     kernelv1.StateCategory_STATE_CATEGORY_TODO,
	"active":   kernelv1.StateCategory_STATE_CATEGORY_ACTIVE,
	"waiting":  kernelv1.StateCategory_STATE_CATEGORY_WAITING,
	"done":     kernelv1.StateCategory_STATE_CATEGORY_DONE,
	"canceled": kernelv1.StateCategory_STATE_CATEGORY_CANCELED,
	"none":     kernelv1.StateCategory_STATE_CATEGORY_NONE,
}

// typeOut is a Type version for the wire, named in namespace ns.
func typeOut(ns string, t *typedef.Type) *kernelv1.Type {
	out := &kernelv1.Type{
		Name: typeName(ns, t.ID, t.Version), TypeId: t.ID, Version: int32(t.Version),
		InitialState: t.InitialState,
	}
	for _, f := range t.Fields {
		out.Fields = append(out.Fields, &kernelv1.Field{
			Id: f.ID, Type: fieldTypes[f.Type], Options: f.Options, WritableBy: fieldWriters[f.WritableBy],
			WritableByRoles: f.WritableByRoles,
		})
	}
	for _, s := range t.States {
		st := &kernelv1.State{Id: s.ID, Category: categories[s.Category], Frozen: s.Frozen}
		// Types are not secret: a client sees what a state will do by itself.
		if s.Timer != nil {
			st.Timer = &kernelv1.StateTimer{After: s.Timer.After, ToState: s.Timer.To}
		}
		if s.At != nil {
			st.At = &kernelv1.AtTimer{Field: s.At.Field, ToState: s.At.To}
		}
		for _, a := range s.Acting {
			st.Acting = append(st.Acting, &kernelv1.ActingTimer{Id: a.ID, After: a.After, Every: a.Every, Max: int32(a.Max)})
		}
		out.States = append(out.States, st)
	}
	for _, tr := range t.Transitions {
		// Whether a check or a held reaction holds the move is the
		// lifecycle's knowledge.
		_, checked := lifecycle.CheckFor(t, tr.From, tr.To)
		if _, held := lifecycle.HeldReaction(t, tr.From, tr.To); held {
			checked = true
		}
		out.Transitions = append(out.Transitions, &kernelv1.Transition{
			FromState: tr.From, ToState: tr.To, Guard: tr.Guard, Required: tr.Required,
			Allowed: tr.Allowed, Except: tr.Except, Escape: tr.Escape, Checked: checked, Clears: tr.Clears,
			Sets: tr.Sets,
		})
	}
	for _, l := range t.Links {
		lt := &kernelv1.LinkType{Name: l.Name, Inverse: l.Inverse, MaxOne: l.SourceSingle(), InverseMaxOne: l.TargetSingle(),
			WritableBy: fieldWriters[l.WritableBy]}
		if r := l.Resolve; r != nil {
			lt.Resolve = &kernelv1.LinkResolve{OnEnter: r.OnEnter, Field: r.Field, Equals: r.Equals, State: r.State}
		}
		for _, target := range l.Targets {
			if target == typedef.AnyType { // "*": any Type, not a Type's name
				lt.Targets = append(lt.Targets, target)
				continue
			}
			lt.Targets = append(lt.Targets, namespaceName(ns)+"/types/"+target)
		}
		out.LinkTypes = append(out.LinkTypes, lt)
	}
	for _, in := range t.Incoming {
		link := &kernelv1.IncomingLink{Name: in.Name, Link: in.Link}
		for _, from := range in.From {
			link.From = append(link.From, namespaceName(ns)+"/types/"+from)
		}
		out.Incoming = append(out.Incoming, link)
	}
	for _, r := range t.Rollups {
		out.Rollups = append(out.Rollups, &kernelv1.Rollup{Id: r.ID, Over: r.Over, CountBy: r.CountBy})
	}
	for _, inv := range t.Invariants {
		out.Invariants = append(out.Invariants, &kernelv1.Invariant{Id: inv.ID, Rule: inv.Rule})
	}
	for _, r := range t.Reactions {
		out.Reactions = append(out.Reactions, &kernelv1.Reaction{
			Id: r.ID, Kind: reactionKinds[r.Kind], State: r.State, FromStates: r.From,
			LinkType: r.Link, Add: r.Add, Undoes: r.Undoes,
		})
	}
	for _, key := range slices.Sorted(maps.Keys(t.AcceptsReactions)) {
		out.AcceptsReactions = append(out.AcceptsReactions,
			&kernelv1.AcceptedReaction{Reaction: key, Fields: t.AcceptsReactions[key]})
	}
	return out
}

var reactionKinds = map[typedef.ReactionKind]kernelv1.ReactionKind{
	typedef.ReactionHeld:  kernelv1.ReactionKind_REACTION_KIND_HELD,
	typedef.ReactionAfter: kernelv1.ReactionKind_REACTION_KIND_AFTER,
}

func (s *server) GetType(ctx context.Context, req *connect.Request[kernelv1.GetTypeRequest]) (*connect.Response[kernelv1.GetTypeResponse], error) {
	p := caller(ctx)
	ns, typeID, version, ok := parseTypeName(req.Msg.Name)
	if !ok {
		return nil, badField("name", "want namespaces/{ns}/types/{type}/versions/{n}")
	}
	if ns != p.Namespace {
		return nil, notFound()
	}
	t, err := s.k.Type(ctx, typeID, version)
	if errors.Is(err, itemservice.ErrInvalid) { // Type's only refusal: not registered
		return nil, notFound()
	}
	if err != nil {
		return nil, errorFor(err, "")
	}
	return connect.NewResponse(&kernelv1.GetTypeResponse{Type: typeOut(ns, t)}), nil
}

func (s *server) ListTypes(ctx context.Context, req *connect.Request[kernelv1.ListTypesRequest]) (*connect.Response[kernelv1.ListTypesResponse], error) {
	p := caller(ctx)
	if cerr := parentNamespace(p, req.Msg.Parent); cerr != nil {
		return nil, cerr
	}
	types, err := s.k.ListTypes(ctx)
	if err != nil {
		return nil, errorFor(err, "")
	}
	out := &kernelv1.ListTypesResponse{Types: make([]*kernelv1.Type, 0, len(types))}
	for _, t := range types {
		out.Types = append(out.Types, typeOut(p.Namespace, t))
	}
	return connect.NewResponse(out), nil
}
