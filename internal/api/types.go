package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
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
		InitialState: t.InitialState, Summary: t.Summary,
	}
	for _, f := range t.Fields {
		out.Fields = append(out.Fields, &kernelv1.Field{
			Id: f.ID, Type: fieldTypes[f.Type], Options: f.Options, WritableBy: fieldWriters[f.WritableBy],
			WritableByRoles: f.WritableByRoles, Multiple: f.Multiple, Max: int32(f.Max),
			UniqueAmongSiblings: f.UniqueAmongSiblings, Label: f.Label, Help: f.Help,
		})
	}
	for _, s := range t.States {
		st := &kernelv1.State{Id: s.ID, Category: categories[s.Category], Frozen: s.Frozen,
			WaitsOnRequester: s.WaitsOn == typedef.WaitsOnRequester, Label: s.Label, Help: s.Help, Due: s.Due,
			Claim: s.Claim}
		// Types are not secret: a client sees what a state will do by itself.
		if s.Timer != nil {
			st.Timer = &kernelv1.StateTimer{After: s.Timer.After, ToState: s.Timer.To}
		}
		if s.At != nil {
			st.At = &kernelv1.AtTimer{Field: s.At.Field, ToState: s.At.To}
		}
		for _, a := range s.Acting {
			st.Acting = append(st.Acting, &kernelv1.ActingTimer{Id: a.ID, After: a.After, Every: a.Every, Max: int32(a.Max),
				AtField: a.At, When: a.When, Sets: a.Sets, Hook: a.Hook})
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
			Sets: tr.Sets, Label: tr.Label, Help: tr.Help, Primary: tr.Primary,
		})
	}
	for _, l := range t.Links {
		lt := &kernelv1.LinkType{Name: l.Name, Inverse: l.Inverse, MaxOne: l.SourceSingle(), InverseMaxOne: l.TargetSingle(),
			WritableBy: fieldWriters[l.WritableBy], WritableByRoles: l.WritableByRoles}
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
		pr := &kernelv1.Reaction{
			Id: r.ID, Kind: reactionKinds[r.Kind], State: r.State, FromStates: r.From,
			LinkType: r.Link, Add: r.Add, Undoes: r.Undoes,
			Trigger: kernelv1.ReactionTrigger_REACTION_TRIGGER_ENTER,
			When:    r.When, By: r.By, ToState: r.To, TargetFromStates: r.TargetFrom, CreateFields: r.Fields,
			Target: r.Target, SetFields: r.Set,
			ForEach: r.ForEach, OnGuard: r.OnGuard,
		}
		if c := r.Children; c != nil {
			pr.ChildrenWhen = c.When
			for _, of := range c.Of {
				pr.ChildrenOfTypes = append(pr.ChildrenOfTypes, namespaceName(ns)+"/types/"+of)
			}
		}
		if r.On == typedef.OnCreatedInside {
			pr.Trigger = kernelv1.ReactionTrigger_REACTION_TRIGGER_CREATED_INSIDE
		}
		for _, of := range r.Of {
			pr.OfTypes = append(pr.OfTypes, namespaceName(ns)+"/types/"+of)
		}
		if r.Kind == typedef.ReactionCreate {
			pr.CreateType = typeName(ns, r.Type, r.Version)
		}
		out.Reactions = append(out.Reactions, pr)
	}
	for _, key := range slices.Sorted(maps.Keys(t.AcceptsReactions)) {
		acc := t.AcceptsReactions[key]
		out.AcceptsReactions = append(out.AcceptsReactions, &kernelv1.AcceptedReaction{
			Reaction: key, Fields: acc.Fields, Moves: acc.Moves, Creates: acc.MayCreate, CreatesFields: acc.Creates,
			Sets: acc.Sets})
	}
	out.ReadableByReactions = t.ReadableByReactions
	out.Own, out.Shared, out.CustomerFields = t.Own, t.Shared, t.CustomerFields
	out.PlacementWritableBy = t.PlacementWritableBy
	// Offers, by name: a map has no order of its own.
	for _, name := range slices.Sorted(maps.Keys(t.Offers)) {
		out.Offers = append(out.Offers, &kernelv1.Offer{Name: name, Field: t.Offers[name]})
	}
	return out
}

var reactionKinds = map[typedef.ReactionKind]kernelv1.ReactionKind{
	typedef.ReactionHeld:   kernelv1.ReactionKind_REACTION_KIND_HELD,
	typedef.ReactionMove:   kernelv1.ReactionKind_REACTION_KIND_MOVE,
	typedef.ReactionCreate: kernelv1.ReactionKind_REACTION_KIND_CREATE,
	typedef.ReactionAfter:  kernelv1.ReactionKind_REACTION_KIND_AFTER,
	typedef.ReactionSet:    kernelv1.ReactionKind_REACTION_KIND_SET,
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

// The limits of one ValidateTypes or ExplainType call (Ventoux V2b; raised
// for build_step, 2026-11-24).
const (
	maxTypeSources     = 200
	maxTypeSourceBytes = 1 << 20 // 1 MiB, in all
)

// draftSources checks a request's draft sources against the limits: 1 to
// 200, each named once, 1 MiB of content in all.
func draftSources(srcs []*kernelv1.TypeSource) ([]typedef.Source, *connect.Error) {
	total := 0
	for _, src := range srcs {
		total += len(src.Content)
	}
	if len(srcs) == 0 || len(srcs) > maxTypeSources || total > maxTypeSourceBytes {
		return nil, overTheLimits(len(srcs), total)
	}
	names := map[string]bool{}
	sources := make([]typedef.Source, 0, len(srcs))
	for _, src := range srcs {
		if src.Name == "" || names[src.Name] {
			return nil, badField("sources", "give each source a name of its own")
		}
		names[src.Name] = true
		sources = append(sources, typedef.Source{Name: src.Name, Content: []byte(src.Content)})
	}
	return sources, nil
}

// overTheLimits refuses a request over the draft limits. It names both
// limits and the request's own count and size, so a tool can split the
// request without guessing. The size is in MiB, rounded up to one decimal:
// a request over 1 MiB never reads "1.0 MiB". A count and a size are not
// what anyone typed, so they may be in a field violation (docs/design.md,
// "Messages are fixed").
func overTheLimits(n, size int) *connect.Error {
	const mib = 1 << 20
	tenths := (size*10 + mib - 1) / mib // integer division, rounded up
	noun := "sources"
	if n == 1 {
		noun = "source"
	}
	return badField("sources", fmt.Sprintf("give 1 to %d sources, %d MiB in all; this request has %d %s, %d.%d MiB",
		maxTypeSources, maxTypeSourceBytes/mib, n, noun, tenths/10, tenths%10))
}

// problemsOut converts a draft's problems, named in the caller's
// namespace, sorted by source, then position, once every source has a name.
func problemsOut(ns string, ps typedef.Problems) []*kernelv1.TypeProblem {
	out := make([]*kernelv1.TypeProblem, 0, len(ps))
	for _, pr := range ps {
		tp := &kernelv1.TypeProblem{
			Source: pr.Source, Path: pr.Path, Line: int32(pr.Line), Column: int32(pr.Column),
			Severity: kernelv1.TypeProblemSeverity_TYPE_PROBLEM_SEVERITY_ERROR,
			Code:     string(pr.Code), Message: pr.Message,
		}
		if pr.TypeID != "" {
			tp.Type = typeName(ns, pr.TypeID, pr.Version)
		}
		if tp.Source == "" { // a registered version's: named in the caller's namespace
			tp.Source = tp.Type
		}
		out = append(out, tp)
	}
	slices.SortStableFunc(out, func(a, b *kernelv1.TypeProblem) int {
		return cmp.Or(strings.Compare(a.Source, b.Source), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
	return out
}

// ValidateTypes checks draft Types against every registered version and
// registers nothing (Ventoux V2b). Users and agents may call it; customers,
// and agents acting for one, may not. Like ListTypes it reads no Items: a
// Type definition holds no Item data, so nothing here goes through
// policy.Project.
func (s *server) ValidateTypes(ctx context.Context, req *connect.Request[kernelv1.ValidateTypesRequest]) (*connect.Response[kernelv1.ValidateTypesResponse], error) {
	p := caller(ctx)
	if cerr := parentNamespace(p, req.Msg.Parent); cerr != nil {
		return nil, cerr
	}
	if item.IsKind(p.Actor, item.KindCustomer) || item.IsKind(p.OnBehalfOf, item.KindCustomer) {
		return nil, apiError(connect.CodePermissionDenied, "NOT_ALLOWED", nil)
	}
	sources, cerr := draftSources(req.Msg.Sources)
	if cerr != nil {
		return nil, cerr
	}
	ps, err := s.k.ValidateTypes(ctx, sources)
	if err != nil {
		return nil, errorFor(err, "")
	}
	out := &kernelv1.ValidateTypesResponse{Problems: problemsOut(p.Namespace, ps)}
	return connect.NewResponse(out), nil
}
