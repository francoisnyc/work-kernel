// What the apps know about their Types: the words for states, fields,
// options and moves. The Types still say which moves exist (moves.ts) and
// the server still decides; these are only the words.
import { StateCategory } from "@gen/kernel/v1/kernel_pb";
import type { Audience } from "./people";

// The Type ID in a Type name: namespaces/acme/types/story/versions/3 -> story.
export function typeIdOf(typeName: string): string {
  return typeName.split("/")[3] ?? "";
}

// The version in a Type name: ... /versions/3 -> 3 (0 when there is none).
export function versionOf(typeName: string): number {
  return Number(typeName.split("/")[5] ?? 0);
}

// "on_hold" -> "On hold".
export function humanize(id: string): string {
  const words = id.replace(/_/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

const ISSUES = new Set(["story", "bug", "task"]);
export const isIssueType = (typeId: string) => ISSUES.has(typeId);

const states: Record<string, string> = {
  todo: "To do",
  in_progress: "In progress",
  in_review: "In review",
  done: "Done",
  canceled: "Canceled",
  waiting_for_customer: "Waiting for customer",
};

const customerStates: Record<string, string> = {
  new: "Received",
  waiting_for_customer: "Waiting for you",
};

// A state's name. Customers see the incident's states in their own words
// (Received, Waiting for you); an unknown state is humanized ("on_hold" ->
// "On hold").
export function stateLabel(typeId: string, state: string, audience: Audience = "staff"): string {
  if (audience === "customer" && typeId === "incident" && customerStates[state]) return customerStates[state];
  return states[state] ?? humanize(state);
}

const fields: Record<string, string> = {
  f_due_first_response: "First response due",
  f_due_resolution: "Resolution due",
};

// A field's name: f_due_resolution -> "Resolution due"; unknown fields are
// humanized without their prefix.
export function fieldLabel(fieldId: string): string {
  return fields[fieldId] ?? humanize(fieldId.replace(/^f_/, ""));
}

const options: Record<string, string> = { wontfix: "Won't fix" };

// An enum option's name: cannot_reproduce -> "Cannot reproduce"; P1 stays P1.
export function optionLabel(_fieldId: string, option: string): string {
  return options[option] ?? humanize(option);
}

// Move labels by Type family, keyed "from>to", then "*>to".
const issueMoves: Record<string, string> = {
  "todo>in_progress": "Start progress",
  "in_progress>in_review": "Send to review",
  "in_review>in_progress": "Back to progress",
  "*>done": "Mark done",
  "done>todo": "Reopen",
  "*>canceled": "Cancel",
  "canceled>todo": "Restore",
};
const moves: Record<string, Record<string, string>> = {
  epic: { "todo>in_progress": "Start", "in_progress>done": "Mark done", "done>in_progress": "Reopen" },
  sprint: { "planned>active": "Start sprint", "active>closed": "Complete sprint" },
  incident: {
    "new>in_progress": "Start work",
    "in_progress>waiting_for_customer": "Wait for customer",
    "waiting_for_customer>in_progress": "Resume work",
    "*>resolved": "Resolve",
    "resolved>in_progress": "Reopen",
    "*>canceled": "Cancel",
  },
};
const customerMoves: Record<string, string> = {
  "new>canceled": "Cancel request",
  "resolved>in_progress": "Reopen request",
};

// The button for a move: "Start work", "Send to review", "Mark done";
// unknown moves are "Move to <state>".
export function transitionLabel(typeId: string, from: string, to: string, audience: Audience = "staff"): string {
  const table = isIssueType(typeId) ? issueMoves : moves[typeId];
  const custom = audience === "customer" && typeId === "incident" ? customerMoves[`${from}>${to}`] : undefined;
  return custom ?? table?.[`${from}>${to}`] ?? table?.[`*>${to}`] ?? `Move to ${stateLabel(typeId, to, audience)}`;
}

// The portal's urgency choices for f_priority, most urgent first; P3 is
// the default.
export const urgencyChoices: { value: string; label: string }[] = [
  { value: "P1", label: "Critical — work has stopped" },
  { value: "P2", label: "High — something important is blocked" },
  { value: "P3", label: "Medium — it's getting in the way" },
  { value: "P4", label: "Low — it can wait" },
];

// The tone a state's category is drawn in.
export type Tone = "neutral" | "active" | "waiting" | "done" | "canceled";
export function categoryTone(category: StateCategory): Tone {
  switch (category) {
    case StateCategory.ACTIVE:
      return "active";
    case StateCategory.WAITING:
      return "waiting";
    case StateCategory.DONE:
      return "done";
    case StateCategory.CANCELED:
      return "canceled";
    default:
      return "neutral";
  }
}

// An issue's key until the kernel has one (Later): "PLAT · 3f9a1c2b", the
// space's key and the LAST 8 characters of the Item's ID. An ID is a
// UUIDv7: its first characters are a timestamp, the same for every Item
// made in the same moment; its last are random.
export function issueKey(spaceKey: string, itemName: string): string {
  const id = itemName.slice(itemName.lastIndexOf("/") + 1).slice(-8);
  return spaceKey === "" ? id : `${spaceKey} · ${id}`;
}
