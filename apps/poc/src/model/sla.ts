// Service levels, read from the goals (sla_goal Items contained in an
// incident) and from the due instants the goals copy onto the incident.
// What a goal records is the truth: breached is its f_breached_at, met or
// missed its f_outcome. The browser computes neither; the only thing it
// adds is Overdue, between the due instant and the breach the goal records.
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { Item, ItemEvent, SearchItemsRequestSchema } from "@gen/kernel/v1/kernel_pb";
import { todo } from "./todo";

export type Metric = "first_response" | "resolution";

// "First response", "Resolution".
export function metricLabel(_metric: string): string {
  return todo();
}

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
export function goalStatus(_goal: Item, _now: Date): GoalStatus {
  return todo();
}

// The incidents with a breached goal still open, and which metric.
export type BreachedIndex = { has(incident: string, metric: Metric): boolean; incidents(): string[] };
export function breachedIndex(_goals: Item[]): BreachedIndex {
  return todo();
}

// The one search that feeds breachedIndex for a queue: open goals (running
// or paused) whose f_breached_at is set. A search cannot ask for the goals
// of one desk (no container filter): this is every open breached goal the
// caller can see.
export function breachSearch(_ns: string): MessageInitShape<typeof SearchItemsRequestSchema> {
  return todo();
}

// A queue cell for one metric of one incident.
export type DueCell =
  | { kind: "countdown"; due: string }
  | { kind: "overdue"; due: string }
  | { kind: "breached" }
  | { kind: "paused" }
  | { kind: "none" };
export function dueCell(_incident: Item, _metric: Metric, _breached: BreachedIndex, _now: Date): DueCell {
  return todo();
}

// One line of a goal's timeline, from its history.
export type GoalEntry = {
  at: string;
  kind: "started" | "paused" | "resumed" | "breached" | "met" | "missed" | "completed" | "canceled";
  // started and resumed: the due instant set.
  due?: string;
};
export function goalTimeline(_events: ItemEvent[]): GoalEntry[] {
  return todo();
}
