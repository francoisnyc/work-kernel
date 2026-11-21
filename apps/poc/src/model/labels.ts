// What the apps know about their Types: the words for states, fields,
// options and moves. The Types still say which moves exist (moves.ts) and
// the server still decides; these are only the words.
import { StateCategory } from "@gen/kernel/v1/kernel_pb";
import type { Audience } from "./people";
import { todo } from "./todo";

// The Type ID in a Type name: namespaces/acme/types/story/versions/3 -> story.
export function typeIdOf(_typeName: string): string {
  return todo();
}

// The version in a Type name: ... /versions/3 -> 3 (0 when there is none).
export function versionOf(_typeName: string): number {
  return todo();
}

// A state's name. Customers see the incident's states in their own words
// (Received, Waiting for you); an unknown state is humanized ("on_hold" ->
// "On hold").
export function stateLabel(_typeId: string, _state: string, _audience?: Audience): string {
  return todo();
}

// A field's name: f_due_resolution -> "Resolution due"; unknown fields are
// humanized without their prefix.
export function fieldLabel(_fieldId: string): string {
  return todo();
}

// An enum option's name: cannot_reproduce -> "Cannot reproduce"; P1 stays P1.
export function optionLabel(_fieldId: string, _option: string): string {
  return todo();
}

// The button for a move: "Start work", "Send to review", "Mark done";
// unknown moves are "Move to <state>".
export function transitionLabel(_typeId: string, _from: string, _to: string, _audience?: Audience): string {
  return todo();
}

// The portal's urgency choices for f_priority, most urgent first; P3 is
// the default.
export const urgencyChoices: { value: string; label: string }[] = [];

// The tone a state's category is drawn in.
export type Tone = "neutral" | "active" | "waiting" | "done" | "canceled";
export function categoryTone(_category: StateCategory): Tone {
  return todo();
}

// An issue's key until the kernel has one: "PLAT · 3f9a1c2b" (the space's
// key and the first 8 characters of the Item's ID).
export function issueKey(_spaceKey: string, _itemName: string): string {
  return todo();
}
