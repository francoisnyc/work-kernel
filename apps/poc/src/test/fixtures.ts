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
  LinkDirection,
  ReactionKind,
  ReactionTrigger,
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
  opts: {
    state?: string;
    fields?: JsonObject;
    container?: string;
    creator?: string;
    onBehalfOf?: string;
    at?: string;
    category?: StateCategory;
    links?: { linkType: string; name: string; restricted?: boolean }[];
    hold?: { check: string; fromState: string; requestedState: string };
  } = {},
): Item {
  return create(ItemSchema, {
    name: itemName(id),
    type,
    state: opts.state ?? "",
    category: opts.category ?? C.UNSPECIFIED,
    fields: opts.fields ?? {},
    container: opts.container ?? "",
    creator: opts.creator ?? "user:sam",
    creatorOnBehalfOf: opts.onBehalfOf ?? "",
    links: (opts.links ?? []).map((l) => ({
      linkType: l.linkType,
      direction: LinkDirection.OUTGOING,
      item: { name: l.restricted ? "" : l.name, restricted: l.restricted ?? false },
    })),
    hold: opts.hold ? { name: `${itemName(id)}/holds/h1`, ...opts.hold } : undefined,
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
    onBehalfOf: string;
    requestedState: string;
    reason: string;
    causationId: string;
    added: JsonObject;
    timer: string;
    run: number;
    hold: string;
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
    onBehalfOf: opts.onBehalfOf ?? "",
    requestedState: opts.requestedState ?? "",
    reason: opts.reason ?? "",
    causationId: opts.causationId ?? "",
    added: opts.added,
    timer: opts.timer ?? "",
    run: opts.run ?? 0,
    hold: opts.hold ?? "",
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

const tr = (
  fromState: string,
  toState: string,
  extra: { required?: string[]; allowed?: string[]; guard?: string; checked?: boolean; escape?: boolean; except?: string[] } = {},
) => ({
  fromState,
  toState,
  required: extra.required ?? [],
  allowed: extra.allowed ?? [],
  guard: extra.guard ?? "",
  checked: extra.checked ?? false,
  escape: extra.escape ?? false,
  except: extra.except ?? [],
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

// vacation_request@3 (types/vacation_request.v3.yaml): the HR check on
// submit, the balance link the kernel sets on entering submitted, the
// reminders and the expiry timer, the held deduction and its refund.
const frozen = ["f_start", "f_end", "f_days", "f_leave_type", "f_approver"];
const vacationFields = [
  field("f_start", FieldType.DATE),
  field("f_end", FieldType.DATE),
  field("f_days", FieldType.INT),
  field("f_leave_type", FieldType.ENUM, { options: ["annual", "unpaid", "other"] }),
  field("f_approver", FieldType.PRINCIPAL, { kernel: true }),
  field("f_comment", FieldType.STRING),
];
const submitGuard = "f_end >= f_start && f_start >= today && f_days > 0";
const submit = tr("draft", "submitted", {
  allowed: ["creator"],
  required: ["f_start", "f_end", "f_days", "f_leave_type"],
  guard: submitGuard,
  checked: true,
});
const decide = (to: string) => tr("submitted", to, { allowed: ["field:f_approver"], except: ["creator"] });

export const vacationType: Type = create(TypeSchema, {
  name: typeName("vacation_request", 3),
  typeId: "vacation_request",
  version: 3,
  initialState: "draft",
  fields: vacationFields,
  states: [
    { id: "draft", category: C.TODO },
    {
      id: "submitted",
      category: C.WAITING,
      frozen,
      timer: { after: "120h", toState: "expired" },
      acting: [{ id: "remind", after: "48h", every: "24h", max: 2, hook: "remind_approver" }],
    },
    { id: "approved", category: C.DONE, frozen },
    { id: "rejected", category: C.CANCELED, frozen },
    { id: "withdrawn", category: C.CANCELED },
    { id: "expired", category: C.CANCELED },
  ],
  transitions: [
    submit,
    decide("approved"),
    decide("rejected"),
    tr("submitted", "draft", { allowed: ["creator"] }),
    tr("submitted", "withdrawn", { allowed: ["creator"], escape: true }),
    tr("draft", "withdrawn", { allowed: ["creator"], escape: true }),
    tr("submitted", "expired", { allowed: ["principal:system:timer"] }),
    tr("approved", "withdrawn", { allowed: ["creator"], guard: "f_start >= today" }),
  ],
  linkTypes: [
    {
      name: "balance",
      inverse: "requests",
      targets: ["leave_balance"],
      maxOne: true,
      writableBy: FieldWriter.KERNEL,
      resolve: { onEnter: "submitted", field: "f_owner", equals: "creator", state: "open" },
    },
  ],
  reactions: [
    {
      id: "deduct",
      kind: ReactionKind.HELD,
      trigger: ReactionTrigger.ENTER,
      state: "approved",
      fromStates: ["submitted"],
      linkType: "balance",
      add: { f_used: "f_days" },
    },
    { id: "refund", kind: ReactionKind.AFTER, trigger: ReactionTrigger.ENTER, state: "withdrawn", fromStates: ["approved"], undoes: "deduct" },
  ],
});

// vacation_request@1 (types/vacation_request.v1.yaml): the HR check only.
export const vacationTypeV1: Type = create(TypeSchema, {
  name: typeName("vacation_request", 1),
  typeId: "vacation_request",
  version: 1,
  initialState: "draft",
  fields: vacationFields,
  states: [
    { id: "draft", category: C.TODO },
    { id: "submitted", category: C.WAITING, frozen },
    { id: "approved", category: C.DONE, frozen },
    { id: "rejected", category: C.CANCELED, frozen },
    { id: "withdrawn", category: C.CANCELED },
  ],
  transitions: [
    submit,
    decide("approved"),
    decide("rejected"),
    tr("submitted", "draft", { allowed: ["creator"] }),
    tr("submitted", "withdrawn", { allowed: ["creator"] }),
    tr("draft", "withdrawn", { allowed: ["creator"], escape: true }),
  ],
});

// leave_balance@1 (types/leave_balance.v1.yaml).
export const balanceType: Type = create(TypeSchema, {
  name: typeName("leave_balance", 1),
  typeId: "leave_balance",
  version: 1,
  initialState: "open",
  fields: [field("f_owner", FieldType.PRINCIPAL), field("f_allowance", FieldType.INT), field("f_used", FieldType.INT, { kernel: true })],
  states: [
    { id: "open", category: C.ACTIVE },
    { id: "closed", category: C.DONE },
  ],
  transitions: [tr("open", "closed", { allowed: ["role:hr_admin"] })],
});

// ticket@6 (types/ticket.v6.yaml): two active and two waiting states,
// timers that move back, a check, a move reaction on a comment and a
// create reaction.
export const ticketType: Type = create(TypeSchema, {
  name: typeName("ticket", 6),
  typeId: "ticket",
  version: 6,
  initialState: "triage",
  states: [
    { id: "triage", category: C.TRIAGE },
    { id: "open", category: C.TODO },
    { id: "in_progress", category: C.ACTIVE, timer: { after: "24h", toState: "open" } },
    { id: "waiting", category: C.WAITING, timer: { after: "72h", toState: "in_progress" } },
    { id: "waiting_for_customer", category: C.WAITING },
    { id: "approved", category: C.ACTIVE },
    { id: "resolved", category: C.DONE },
    { id: "canceled", category: C.CANCELED },
  ],
  transitions: [
    tr("triage", "open"),
    tr("triage", "canceled"),
    tr("open", "in_progress", { guard: "f_assignee != null" }),
    tr("in_progress", "open"),
    tr("in_progress", "waiting"),
    tr("waiting", "in_progress"),
    tr("in_progress", "waiting_for_customer"),
    tr("waiting_for_customer", "in_progress", { allowed: ["role:support"], guard: "f_assignee != null" }),
    tr("waiting_for_customer", "canceled"),
    tr("in_progress", "approved", { required: ["f_customer_id", "f_amount"], guard: "f_on_hold != true", allowed: ["role:approver"], checked: true }),
    tr("in_progress", "canceled", { escape: true }),
    tr("in_progress", "resolved", { required: ["f_resolution"] }),
    tr("approved", "resolved", { required: ["f_resolution"] }),
    tr("resolved", "open", { guard: 'f_resolution != "wontfix"' }),
    tr("open", "canceled"),
  ],
  reactions: [
    {
      id: "reopen",
      kind: ReactionKind.MOVE,
      trigger: ReactionTrigger.CREATED_INSIDE,
      ofTypes: [`${NS}/types/comment`],
      when: "child.f_public == true",
      by: "creator",
      toState: "in_progress",
      targetFromStates: ["waiting_for_customer"],
    },
    {
      id: "open_checklist",
      kind: ReactionKind.CREATE,
      trigger: ReactionTrigger.ENTER,
      state: "in_progress",
      fromStates: ["open"],
      createType: typeName("checklist", 1),
    },
  ],
});
