// Items, Types and events as the API returns them, for the unit tests.
// The Types are copies of the real ones (types/), as GetType shows them.
import { create, type JsonObject } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  FieldType,
  FieldWriter,
  ItemEventKind,
  ItemEventSchema,
  ItemSchema,
  StateCategory,
  TypeSchema,
  type Item,
  type ItemEvent,
  type Type,
} from "@gen/kernel/v1/kernel_pb";

export const NS = "namespaces/acme";
export const typeName = (id: string, v: number) => `${NS}/types/${id}/versions/${v}`;
export const itemName = (id: string) => `${NS}/items/${id}`;

const C = StateCategory;

export function item(
  id: string,
  type: string,
  opts: { state?: string; fields?: JsonObject; container?: string; creator?: string; at?: string; category?: StateCategory } = {},
): Item {
  return create(ItemSchema, {
    name: itemName(id),
    type,
    state: opts.state ?? "",
    category: opts.category ?? C.UNSPECIFIED,
    fields: opts.fields ?? {},
    container: opts.container ?? "",
    creator: opts.creator ?? "user:sam",
    createTime: timestampFromDate(new Date(opts.at ?? "2026-11-21T09:00:00Z")),
    etag: "c1",
  });
}

export function event(
  kind: ItemEventKind,
  at: string,
  opts: Partial<{
    actor: string;
    fromState: string;
    toState: string;
    fields: JsonObject;
    setOps: JsonObject;
    linkType: string;
    linkTarget: string;
    reaction: string;
    hook: string;
    seq: number;
  }> = {},
): ItemEvent {
  return create(ItemEventSchema, {
    kind,
    createTime: timestampFromDate(new Date(at)),
    itemSeq: BigInt(opts.seq ?? 1),
    actor: opts.actor ?? "user:sam",
    fromState: opts.fromState ?? "",
    toState: opts.toState ?? "",
    fields: opts.fields,
    setOps: opts.setOps,
    linkType: opts.linkType ?? "",
    linkTarget: opts.linkTarget ? { name: opts.linkTarget, restricted: false } : undefined,
    reaction: opts.reaction ?? "",
    hook: opts.hook ?? "",
  });
}

export const Kind = ItemEventKind;

const field = (id: string, type: FieldType, extra: { options?: string[]; kernel?: boolean; multiple?: boolean } = {}) => ({
  id,
  type,
  options: extra.options ?? [],
  writableBy: extra.kernel ? FieldWriter.KERNEL : FieldWriter.UNSPECIFIED,
  multiple: extra.multiple ?? false,
});

const tr = (fromState: string, toState: string, extra: { required?: string[]; allowed?: string[]; guard?: string } = {}) => ({
  fromState,
  toState,
  required: extra.required ?? [],
  allowed: extra.allowed ?? [],
  guard: extra.guard ?? "",
});

// incident@2 (types/incident.v2.yaml).
export const incidentType: Type = create(TypeSchema, {
  name: typeName("incident", 2),
  typeId: "incident",
  version: 2,
  initialState: "new",
  fields: [
    field("f_title", FieldType.STRING),
    field("f_priority", FieldType.ENUM, { options: ["P1", "P2", "P3", "P4"] }),
    field("f_impact", FieldType.ENUM, { options: ["extensive", "significant", "moderate", "minor"] }),
    field("f_assignee", FieldType.PRINCIPAL),
    field("f_resolution", FieldType.ENUM, { options: ["fixed", "workaround", "wontfix", "duplicate"] }),
    field("f_due_first_response", FieldType.DATETIME, { kernel: true }),
    field("f_due_resolution", FieldType.DATETIME, { kernel: true }),
  ],
  states: [
    { id: "new", category: C.TRIAGE },
    { id: "in_progress", category: C.ACTIVE },
    { id: "waiting_for_customer", category: C.WAITING },
    { id: "resolved", category: C.DONE, timer: { after: "120h", toState: "closed" } },
    { id: "closed", category: C.DONE },
    { id: "canceled", category: C.CANCELED },
  ],
  transitions: [
    tr("new", "in_progress"),
    tr("in_progress", "waiting_for_customer"),
    tr("waiting_for_customer", "in_progress"),
    tr("in_progress", "resolved", { required: ["f_resolution"] }),
    tr("waiting_for_customer", "resolved", { required: ["f_resolution"] }),
    tr("resolved", "closed", { allowed: ["principal:system:timer"] }),
    tr("resolved", "in_progress", { allowed: ["creator", "role:desk_agent"] }),
    tr("new", "canceled", { allowed: ["creator", "role:desk_agent"] }),
    tr("in_progress", "canceled"),
  ],
  shared: ["reply"],
  customerFields: ["f_title", "f_priority", "f_impact", "f_resolution"],
});

// story@3 (types/story.v3.yaml; bug and task are the same).
export const storyType: Type = create(TypeSchema, {
  name: typeName("story", 3),
  typeId: "story",
  version: 3,
  initialState: "todo",
  fields: [
    field("f_summary", FieldType.STRING),
    field("f_description", FieldType.STRING),
    field("f_assignee", FieldType.PRINCIPAL),
    field("f_priority", FieldType.ENUM, { options: ["P1", "P2", "P3", "P4"] }),
    field("f_points", FieldType.INT),
    field("f_resolution", FieldType.ENUM, { options: ["fixed", "wontfix", "duplicate", "cannot_reproduce"] }),
    field("f_rank", FieldType.STRING),
    field("f_resolved_at", FieldType.DATETIME, { kernel: true }),
    field("f_labels", FieldType.STRING, { multiple: true }),
  ],
  states: [
    { id: "todo", category: C.TODO },
    { id: "in_progress", category: C.ACTIVE },
    { id: "in_review", category: C.ACTIVE },
    { id: "done", category: C.DONE },
    { id: "canceled", category: C.CANCELED },
  ],
  transitions: [
    tr("todo", "in_progress"),
    tr("in_progress", "in_review"),
    tr("in_review", "in_progress"),
    tr("in_review", "done", { required: ["f_resolution"] }),
    tr("in_progress", "done", { required: ["f_resolution"] }),
    tr("done", "todo"),
    tr("todo", "canceled"),
    tr("in_progress", "canceled"),
    tr("in_review", "canceled"),
    tr("canceled", "todo"),
  ],
});

// epic@1.
export const epicType: Type = create(TypeSchema, {
  name: typeName("epic", 1),
  typeId: "epic",
  version: 1,
  initialState: "todo",
  states: [
    { id: "todo", category: C.TODO },
    { id: "in_progress", category: C.ACTIVE },
    { id: "done", category: C.DONE },
  ],
  transitions: [
    tr("todo", "in_progress"),
    tr("in_progress", "done", { guard: 'links.issues.all(i, i.category in ["done", "canceled"])' }),
    tr("done", "in_progress"),
  ],
});

// A goal of the sla package, contained in an incident.
export function goal(
  id: string,
  incident: string,
  state: "running" | "paused" | "completed" | "canceled",
  fields: JsonObject,
): Item {
  return item(id, typeName("sla_goal", 1), { state, container: itemName(incident), fields, creator: "reaction:incident:sla_start" });
}
