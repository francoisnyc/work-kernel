// Types and Items for the unit tests, shaped like the real ones in types/
// (ticket v5, vacation_request v3, leave_balance, story, sprint, epic,
// subscription): the UI is generic, so the tests use several Types.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { ConnectError, type Code } from "@connectrpc/connect";
import { ErrorInfoSchema } from "../gen/google/rpc/error_details_pb";
import {
  FieldType,
  FieldWriter,
  ItemSchema,
  LinkDirection,
  StateCategory,
  TypeSchema,
  type Item,
  type Type,
} from "../gen/kernel/v1/kernel_pb";

export const NS = "namespaces/acme";
export const typeName = (id: string, version = 1) => `${NS}/types/${id}/versions/${version}`;
export const itemName = (n: number) => `${NS}/items/${String(n).padStart(8, "0")}-0000-4000-8000-000000000000`;

const t = (init: MessageInitShape<typeof TypeSchema>): Type => create(TypeSchema, init);

export const ticket = t({
  name: typeName("ticket", 5),
  typeId: "ticket",
  version: 5,
  initialState: "triage",
  fields: [
    { id: "f_title", type: FieldType.STRING },
    { id: "f_priority", type: FieldType.INT },
    { id: "f_reporter", type: FieldType.STRING },
    { id: "f_assignee", type: FieldType.PRINCIPAL },
    { id: "f_urgent", type: FieldType.BOOL },
    { id: "f_resolution", type: FieldType.ENUM, options: ["fixed", "wontfix", "duplicate"] },
    { id: "f_customer_id", type: FieldType.STRING },
    { id: "f_amount", type: FieldType.INT },
    { id: "f_due", type: FieldType.DATE },
  ],
  states: [
    { id: "triage", category: StateCategory.TRIAGE },
    { id: "open", category: StateCategory.TODO },
    { id: "in_progress", category: StateCategory.ACTIVE },
    { id: "approved", category: StateCategory.ACTIVE },
    { id: "resolved", category: StateCategory.DONE },
    { id: "canceled", category: StateCategory.CANCELED },
  ],
  transitions: [
    { fromState: "triage", toState: "open" },
    { fromState: "triage", toState: "canceled" },
    { fromState: "open", toState: "in_progress", guard: "f_assignee != null" },
    { fromState: "in_progress", toState: "open" },
    {
      fromState: "in_progress",
      toState: "approved",
      required: ["f_customer_id", "f_amount"],
      guard: "f_on_hold != true",
      allowed: ["role:approver"],
      checked: true,
    },
    { fromState: "in_progress", toState: "resolved", required: ["f_resolution"] },
    { fromState: "resolved", toState: "open", clears: ["f_resolution"] },
  ],
  linkTypes: [
    { name: "blocks", inverse: "blocked_by", targets: [`${NS}/types/ticket`] },
    { name: "duplicate_of", inverse: "duplicates", targets: [`${NS}/types/ticket`], maxOne: true },
  ],
});

// An older version of the same Type: fewer fields.
export const ticket4 = t({
  name: typeName("ticket", 4),
  typeId: "ticket",
  version: 4,
  initialState: "triage",
  fields: [{ id: "f_title", type: FieldType.STRING }],
  states: [{ id: "triage", category: StateCategory.TRIAGE }],
});

const frozenTerms = ["f_start", "f_end", "f_days", "f_leave_type", "f_approver"];

export const vacation = t({
  name: typeName("vacation_request", 3),
  typeId: "vacation_request",
  version: 3,
  initialState: "draft",
  fields: [
    { id: "f_start", type: FieldType.DATE },
    { id: "f_end", type: FieldType.DATE },
    { id: "f_days", type: FieldType.INT },
    { id: "f_leave_type", type: FieldType.ENUM, options: ["annual", "unpaid", "other"] },
    { id: "f_approver", type: FieldType.PRINCIPAL, writableBy: FieldWriter.KERNEL },
    { id: "f_comment", type: FieldType.STRING },
  ],
  states: [
    { id: "draft", category: StateCategory.TODO },
    { id: "submitted", category: StateCategory.WAITING, frozen: frozenTerms },
    { id: "approved", category: StateCategory.DONE, frozen: frozenTerms },
    { id: "rejected", category: StateCategory.CANCELED, frozen: frozenTerms },
    { id: "withdrawn", category: StateCategory.CANCELED },
    { id: "expired", category: StateCategory.CANCELED },
  ],
  transitions: [
    {
      fromState: "draft",
      toState: "submitted",
      allowed: ["creator"],
      required: ["f_start", "f_end", "f_days", "f_leave_type"],
      guard: "f_end >= f_start && f_start >= today && f_days > 0",
      checked: true,
    },
    { fromState: "submitted", toState: "approved", allowed: ["field:f_approver"], except: ["creator"] },
    { fromState: "submitted", toState: "rejected", allowed: ["field:f_approver"], except: ["creator"] },
    { fromState: "submitted", toState: "draft", allowed: ["creator"] },
    { fromState: "submitted", toState: "withdrawn", allowed: ["creator"], escape: true },
    { fromState: "submitted", toState: "expired", allowed: ["principal:system:timer"] },
  ],
  linkTypes: [
    {
      name: "balance",
      inverse: "requests",
      targets: [`${NS}/types/leave_balance`],
      maxOne: true,
      writableBy: FieldWriter.KERNEL,
    },
  ],
});

// Version 1 names the same approver rule from the same state.
export const vacation1 = t({
  name: typeName("vacation_request", 1),
  typeId: "vacation_request",
  version: 1,
  initialState: "draft",
  fields: [{ id: "f_approver", type: FieldType.PRINCIPAL, writableBy: FieldWriter.KERNEL }],
  states: [
    { id: "draft", category: StateCategory.TODO },
    { id: "submitted", category: StateCategory.WAITING },
    { id: "approved", category: StateCategory.DONE },
  ],
  transitions: [{ fromState: "submitted", toState: "approved", allowed: ["field:f_approver"], except: ["creator"] }],
});

export const leaveBalance = t({
  name: typeName("leave_balance"),
  typeId: "leave_balance",
  version: 1,
  initialState: "open",
  fields: [
    { id: "f_owner", type: FieldType.PRINCIPAL, writableByRoles: ["hr_admin"] },
    { id: "f_allowance", type: FieldType.INT, writableByRoles: ["hr_admin"] },
    { id: "f_used", type: FieldType.INT, writableBy: FieldWriter.KERNEL },
  ],
  states: [
    { id: "open", category: StateCategory.ACTIVE },
    { id: "closed", category: StateCategory.DONE },
  ],
  transitions: [{ fromState: "open", toState: "closed", allowed: ["role:hr_admin"] }],
});

const issueLinks = [
  { name: "epic", inverse: "issues", targets: [`${NS}/types/epic`], maxOne: true },
  { name: "sprint", inverse: "sprint_issues", targets: [`${NS}/types/sprint`], maxOne: true },
  { name: "blocks", inverse: "blocked_by", targets: [`${NS}/types/story`, `${NS}/types/bug`] },
];

const issue = (id: string) =>
  t({
    name: typeName(id),
    typeId: id,
    version: 1,
    initialState: "todo",
    fields: [
      { id: "f_summary", type: FieldType.STRING },
      { id: "f_points", type: FieldType.INT },
      { id: "f_rank", type: FieldType.STRING },
    ],
    states: [
      { id: "todo", category: StateCategory.TODO },
      { id: "done", category: StateCategory.DONE },
    ],
    transitions: [{ fromState: "todo", toState: "done" }],
    linkTypes: issueLinks,
  });

export const story = issue("story");
export const bug = issue("bug");

// A sprint declares no links and no `incoming`: what points to it is found
// in the other Types.
export const sprint = t({
  name: typeName("sprint"),
  typeId: "sprint",
  version: 1,
  initialState: "planned",
  fields: [
    { id: "f_name", type: FieldType.STRING },
    { id: "f_end", type: FieldType.DATE },
  ],
  states: [{ id: "planned", category: StateCategory.BACKLOG }],
});

// Version 2 (Jira part 2, step 1): an end instant, and the active state
// closes itself when it passes. Not in allTypes: the tests that count Types
// were written for version 1.
export const sprint2 = t({
  name: typeName("sprint", 2),
  typeId: "sprint",
  version: 2,
  initialState: "planned",
  fields: [
    { id: "f_name", type: FieldType.STRING },
    { id: "f_end", type: FieldType.DATE },
    { id: "f_end_at", type: FieldType.DATETIME },
  ],
  states: [
    { id: "planned", category: StateCategory.BACKLOG },
    { id: "active", category: StateCategory.ACTIVE, at: { field: "f_end_at", toState: "closed" } },
    { id: "review", category: StateCategory.ACTIVE, timer: { after: "48h", toState: "closed" } },
    { id: "closed", category: StateCategory.DONE },
  ],
  transitions: [
    { fromState: "planned", toState: "active" },
    { fromState: "active", toState: "closed" },
    { fromState: "active", toState: "review" },
    { fromState: "review", toState: "closed" },
  ],
});

// Version 2 of a story: done stamps f_resolved_at, reopening unstamps it.
export const story2 = t({
  name: typeName("story", 2),
  typeId: "story",
  version: 2,
  initialState: "todo",
  fields: [
    { id: "f_summary", type: FieldType.STRING },
    { id: "f_resolution", type: FieldType.ENUM, options: ["fixed", "wontfix"] },
    { id: "f_resolved_at", type: FieldType.DATETIME, writableBy: FieldWriter.KERNEL },
  ],
  states: [
    { id: "todo", category: StateCategory.TODO },
    { id: "done", category: StateCategory.DONE },
  ],
  transitions: [
    { fromState: "todo", toState: "done", required: ["f_resolution"], sets: { f_resolved_at: "cause.at" } },
    { fromState: "done", toState: "todo", clears: ["f_resolution"], sets: { f_resolved_at: "null" } },
  ],
});

// Version 3 of a story (Jira part 2, step 2): multi-value fields. Not in
// allTypes, like the other later versions.
export const story3 = t({
  name: typeName("story", 3),
  typeId: "story",
  version: 3,
  initialState: "todo",
  fields: [
    { id: "f_summary", type: FieldType.STRING },
    { id: "f_labels", type: FieldType.STRING, multiple: true, max: 3 },
    { id: "f_colors", type: FieldType.ENUM, multiple: true, options: ["red", "green", "blue"] },
    { id: "f_reviewers", type: FieldType.PRINCIPAL, multiple: true },
    { id: "f_days", type: FieldType.DATE, multiple: true },
    { id: "f_stamps", type: FieldType.STRING, multiple: true, writableBy: FieldWriter.KERNEL },
  ],
  states: [
    { id: "todo", category: StateCategory.TODO },
    { id: "done", category: StateCategory.DONE },
  ],
  transitions: [{ fromState: "todo", toState: "done" }],
});

export const epic = t({
  name: typeName("epic"),
  typeId: "epic",
  version: 1,
  initialState: "todo",
  fields: [{ id: "f_summary", type: FieldType.STRING }],
  states: [{ id: "todo", category: StateCategory.TODO }],
  incoming: [{ name: "issues", link: "epic", from: [`${NS}/types/story`, `${NS}/types/bug`] }],
  rollups: [{ id: "progress", over: "issues", countBy: "category" }],
});

// Watches anything: targets ['*'].
export const subscription = t({
  name: typeName("subscription"),
  typeId: "subscription",
  version: 1,
  linkTypes: [{ name: "watches", inverse: "watched_by", targets: ["*"], maxOne: true }],
});

export const allTypes = [bug, epic, leaveBalance, sprint, story, subscription, ticket4, ticket, vacation1, vacation];

export function item(init: MessageInitShape<typeof ItemSchema>): Item {
  return create(ItemSchema, { etag: "c1", ...init });
}

export const aTicket = item({
  name: itemName(1),
  type: ticket.name,
  state: "in_progress",
  category: StateCategory.ACTIVE,
  fields: { f_title: "printer on fire", f_priority: "2", f_reporter: "tom", f_urgent: true, f_customer_id: "cust-good" },
  links: [
    { linkType: "blocks", direction: LinkDirection.OUTGOING, item: { name: itemName(2) } },
    { linkType: "blocks", direction: LinkDirection.OUTGOING, item: { restricted: true } },
    { linkType: "blocked_by", direction: LinkDirection.INCOMING, item: { name: itemName(3) } },
  ],
});

export const aSubmittedVacation = item({
  name: itemName(10),
  type: vacation.name,
  state: "submitted",
  category: StateCategory.WAITING,
  fields: {
    f_start: "2026-12-01",
    f_end: "2026-12-05",
    f_days: "5",
    f_leave_type: "annual",
    f_approver: "user:bob",
    f_comment: "skiing",
  },
  links: [{ linkType: "balance", direction: LinkDirection.OUTGOING, item: { name: itemName(11) } }],
});

// An error as the item service sends it: a code, and an ErrorInfo with the
// stable reason and its metadata.
export function refusal(code: Code, reason: string, metadata: Record<string, string> = {}): ConnectError {
  return new ConnectError("the fixed message of this reason", code, undefined, [
    { desc: ErrorInfoSchema, value: { reason, domain: "pli.francoisnyc.github.io", metadata } },
  ]);
}
