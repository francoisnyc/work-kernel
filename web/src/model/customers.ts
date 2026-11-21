// Jira part 2, step 4a: what the generic UI needs to know about customers,
// read from the principal and the Types, never from a Type's name. The
// server decides; these only keep the UI from offering what it would refuse.
import { clone } from "@bufbuild/protobuf";
import { TypeSchema, type Transition, type Type } from "../gen/kernel/v1/kernel_pb";
import type { DevUser } from "./devusers";
import { latestVersions } from "./typeinfo";

// isCustomer: the dev user is a customer:<id> principal.
export function isCustomer(u: Pick<DevUser, "principal">): boolean {
  return u.principal.startsWith("customer:");
}

// pickerLabel: how the act-as picker names a dev user.
export function pickerLabel(u: DevUser): string {
  return isCustomer(u) ? `${u.name} (customer)` : u.name;
}

// creatableByCustomer: the Types (latest version of each ID) a customer may
// create inside an Item of the container's Type: its own and shared ones.
// None at the top level.
export function creatableByCustomer(types: Type[], container: Type | undefined): Type[] {
  if (!container) return [];
  const admitted = new Set([...container.own, ...container.shared]);
  return latestVersions(types).filter((t) => admitted.has(t.typeId));
}

// customerMayTake: a customer takes a transition only when its allowed
// names creator (the server still decides).
export function customerMayTake(t: Transition): boolean {
  return t.allowed.includes("creator");
}

// customerView: a Type as a customer meets it: only the fields and link
// names it lists for them, and only the moves they may take, whose
// field:X entries keep only the fields they see (so the Inbox never asks
// about a field hidden from them).
export function customerView(t: Type): Type {
  const shown = new Set(t.customerFields);
  const v = clone(TypeSchema, t);
  v.fields = v.fields.filter((f) => shown.has(f.id));
  v.linkTypes = v.linkTypes.filter((l) => shown.has(l.name));
  v.transitions = v.transitions.filter(customerMayTake);
  for (const tr of v.transitions) {
    tr.allowed = tr.allowed.filter((a) => !a.startsWith("field:") || shown.has(a.slice("field:".length)));
  }
  return v;
}
