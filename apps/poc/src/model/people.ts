// How the apps name people. The API has no directory and no display names:
// a principal is shown by its ID.
import { todo } from "./todo";

export type Audience = "staff" | "customer";

// "user:sam" -> "sam"; "customer:kim" -> "kim"; "agent:triage-bot" ->
// "triage-bot (agent)"; the kernel's own actors (system:..., reaction:...,
// hook:...) -> "System".
export function personName(_principal: string): string {
  return todo();
}

// One or two capital letters for an avatar: "sam" -> "S", "triage-bot" -> "TB".
export function initials(_principal: string): string {
  return todo();
}

// The author of a message as the viewer sees it: "You" for the viewer;
// to a customer, staff are "sam · Support"; to staff, a customer is
// "kim · Customer"; anyone else by personName.
export function authorLabel(_principal: string, _viewer: string, _audience: Audience): string {
  return todo();
}
