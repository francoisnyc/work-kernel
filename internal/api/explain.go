package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// ExplainType reads a Type version in plain language (Ventoux item 6): a
// registered version by name, or a draft from sources, checked as
// ValidateTypes checks drafts (a draft with problems gets them, and no
// reading). Users and agents may call it; customers, and agents acting for
// one, may not: a reading names the organisation's integrations, as
// ListActions does. It reads no Item, so nothing goes through
// policy.Project; the item service gained no method.
func (s *server) ExplainType(ctx context.Context, req *connect.Request[kernelv1.ExplainTypeRequest]) (*connect.Response[kernelv1.ExplainTypeResponse], error) {
	p := caller(ctx)
	m := req.Msg
	draft := m.Parent != "" || len(m.Sources) > 0 || m.TypeId != "" || m.Version != 0
	switch {
	case m.Name == "" && !draft:
		return nil, badField("name", "give a registered version's name, or a draft: parent, sources, type_id and version")
	case m.Name != "" && draft:
		return nil, badField("name", "give a registered version's name or a draft, not both")
	}
	if item.IsKind(p.Actor, item.KindCustomer) || item.IsKind(p.OnBehalfOf, item.KindCustomer) {
		return nil, apiError(connect.CodePermissionDenied, "NOT_ALLOWED", nil)
	}
	if !draft {
		ns, typeID, version, ok := parseTypeName(m.Name)
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
		return connect.NewResponse(&kernelv1.ExplainTypeResponse{Reading: readingOut(ns, t)}), nil
	}

	if cerr := parentNamespace(p, m.Parent); cerr != nil {
		return nil, cerr
	}
	switch {
	case m.TypeId == "":
		return nil, badField("type_id", "name the draft Type to read")
	case m.Version < 1:
		return nil, badField("version", "name the draft version to read")
	}
	sources, cerr := draftSources(m.Sources)
	if cerr != nil {
		return nil, cerr
	}
	ps, err := s.k.ValidateTypes(ctx, sources)
	if err != nil {
		return nil, errorFor(err, "")
	}
	if len(ps) > 0 {
		return connect.NewResponse(&kernelv1.ExplainTypeResponse{Problems: problemsOut(p.Namespace, ps)}), nil
	}
	// Sound drafts: each loads on its own, and the reading reads one Type.
	for _, src := range sources {
		t, err := typedef.Parse(src.Content)
		if err == nil && t.ID == m.TypeId && t.Version == int(m.Version) {
			return connect.NewResponse(&kernelv1.ExplainTypeResponse{Reading: readingOut(p.Namespace, t)}), nil
		}
	}
	return nil, badField("type_id", "no source holds that Type version")
}

// readingOut is a reading as the API serves it, named in the caller's
// namespace.
func readingOut(ns string, t *typedef.Type) *kernelv1.TypeReading {
	r := reading.Read(t, contracts.Default())
	out := &kernelv1.TypeReading{
		Type:       typeName(ns, t.ID, t.Version),
		Fields:     r.Fields,
		NotCovered: r.NotCovered,
		Text:       r.Text(),
	}
	for _, k := range r.Key {
		out.Key = append(out.Key, &kernelv1.ReadingTerm{Name: k.Name, Meaning: k.Meaning})
	}
	for _, sec := range r.Sections {
		so := &kernelv1.ReadingSection{Title: sec.Title}
		for _, sn := range sec.Sentences {
			parts := make([]string, len(sn.Parts))
			for i, p := range sn.Parts {
				parts[i] = string(p)
			}
			so.Sentences = append(so.Sentences, &kernelv1.ReadingSentence{
				Number: int32(sn.N), Kind: string(sn.Kind), Parts: parts, Text: sn.Text, Paths: sn.Paths,
			})
		}
		out.Sections = append(out.Sections, so)
	}
	for _, a := range r.Actions {
		ao := &kernelv1.ReadingAction{Action: a.Action, SideEffects: a.SideEffects, Risk: riskOut[a.Risk], Description: a.Description}
		for _, k := range a.Kinds {
			ao.Kinds = append(ao.Kinds, kindOut[hookproto.Kind(k)])
		}
		out.Actions = append(out.Actions, ao)
	}
	return out
}
