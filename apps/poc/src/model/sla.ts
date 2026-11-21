// Service levels, read from the goals (sla_goal Items contained in an
// incident) and from the due instants the goals copy onto the incident.
// What a goal records is the truth: breached is its f_breached_at, met or
// missed its f_outcome. The browser computes neither; the only thing it
// adds is Overdue, between the due instant and the breach the goal records.
import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ItemEventKind, type Item, type ItemEvent, type SearchItemsRequestSchema } from "@gen/kernel/v1/kernel_pb";
import { parseInstant } from "./countdown";

export type Metric = "first_response" | "resolution";

const metrics: Record<string, string> = { first_response: "First response", resolution: "Resolution" };

// "First response", "Resolution".
export function metricLabel(metric: string): string {
  return metrics[metric] ?? metric;
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");

export type GoalStatus =
  | { kind: "running"; due: string }
  | { kind: "overdue"; due: string }
  | { kind: "breached"; breachedAt: string; paused: boolean }
  | { kind: "paused" }
  | { kind: "met" }
  | { kind: "missed" }
  // Completed without an outcome recorded (never seen; shown as Completed).
  | { kind: "completed" }
  | { kind: "canceled" };

// One goal's status now.
export function goalStatus(goal: Item, now: Date): GoalStatus {
  const f = goal.fields ?? {};
  const breachedAt = str(f.f_breached_at);
  const due = str(f.f_due_at);
  switch (goal.state) {
    case "completed": {
      const outcome = str(f.f_outcome);
      return outcome === "met" ? { kind: "met" } : outcome === "missed" ? { kind: "missed" } : { kind: "completed" };
    }
    case "canceled":
      return { kind: "canceled" };
    case "paused":
      return breachedAt !== "" ? { kind: "breached", breachedAt, paused: true } : { kind: "paused" };
    default:
      if (breachedAt !== "") return { kind: "breached", breachedAt, paused: false };
      if (due !== "" && parseInstant(due) <= now.getTime()) return { kind: "overdue", due };
      return { kind: "running", due };
  }
}

// The incidents with a breached goal still open, and which metric.
export type BreachedIndex = { has(incident: string, metric: Metric): boolean; incidents(): string[] };
export function breachedIndex(goals: Item[]): BreachedIndex {
  const by = new Map<string, Set<string>>();
  for (const g of goals) {
    if (str(g.fields?.f_breached_at) === "") continue;
    const set = by.get(g.container) ?? new Set<string>();
    set.add(str(g.fields?.f_metric));
    by.set(g.container, set);
  }
  return {
    has: (incident, metric) => by.get(incident)?.has(metric) ?? false,
    incidents: () => [...by.keys()],
  };
}

// The one search that feeds breachedIndex for a queue: open goals (running
// or paused) whose f_breached_at is set. A search cannot ask for the goals
// of one desk (no container filter): this is every open breached goal the
// caller can see.
export function breachSearch(ns: string): MessageInitShape<typeof SearchItemsRequestSchema> {
  return {
    parent: ns,
    filter: { types: [`${ns}/types/sla_goal`], states: ["running", "paused"], fields: [{ field: "f_breached_at", notEmpty: true }] },
    pageSize: 500,
  };
}

// A queue cell for one metric of one incident.
export type DueCell =
  | { kind: "countdown"; due: string }
  | { kind: "overdue"; due: string }
  | { kind: "breached" }
  | { kind: "paused" }
  | { kind: "none" };
export function dueCell(incident: Item, metric: Metric, breached: BreachedIndex, now: Date): DueCell {
  if (breached.has(incident.name, metric)) return { kind: "breached" };
  const due = str(incident.fields?.[metric === "resolution" ? "f_due_resolution" : "f_due_first_response"]);
  if (due !== "") return parseInstant(due) <= now.getTime() ? { kind: "overdue", due } : { kind: "countdown", due };
  // The queue reads no goal for this: waiting for the customer is when the
  // resolution goal is paused and shows no due instant.
  if (metric === "resolution" && incident.state === "waiting_for_customer") return { kind: "paused" };
  return { kind: "none" };
}

// One line of a goal's timeline, from its history.
export type GoalEntry = {
  at: string;
  kind: "started" | "paused" | "resumed" | "breached" | "met" | "missed" | "completed" | "canceled";
  // started and resumed: the due instant set.
  due?: string;
};

export function goalTimeline(events: ItemEvent[]): GoalEntry[] {
  const out: GoalEntry[] = [];
  for (const e of events) {
    const at = e.createTime ? timestampDate(e.createTime).toISOString() : "";
    const f = e.fields ?? {};
    const due = str(f.f_due_at);
    switch (e.kind) {
      case ItemEventKind.CREATED:
        out.push(due !== "" ? { at, kind: "started", due } : { at, kind: "started" });
        break;
      case ItemEventKind.TRANSITIONED:
        if (e.toState === "paused") out.push({ at, kind: "paused" });
        else if (e.toState === "running") out.push(due !== "" ? { at, kind: "resumed", due } : { at, kind: "resumed" });
        else if (e.toState === "canceled") out.push({ at, kind: "canceled" });
        else if (e.toState === "completed") {
          const o = str(f.f_outcome);
          out.push({ at, kind: o === "met" ? "met" : o === "missed" ? "missed" : "completed" });
        }
        break;
      case ItemEventKind.UPDATED:
        if (str(f.f_breached_at) !== "") out.push({ at: str(f.f_breached_at), kind: "breached" });
        break;
    }
  }
  return out;
}
