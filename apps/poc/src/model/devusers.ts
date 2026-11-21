// DEV ONLY. The people the sign-in screen offers, read from the dev-token
// file the item service, pli and web/ use (dev-tokens.example.yaml), which
// the stack's poc container serves. Never in a production build: the
// sign-in list is compiled in only with VITE_DEV_USERS=1.
//
// There is no "who am I" in the API: a person's roles come from this file.
import { todo } from "./todo";

export type DevUser = { name: string; token: string; principal: string; namespace: string; roles: string[] };

// The three areas of the app.
export type Area = "desk" | "tracker" | "portal";

// The file's users and customers, in the file's order. Agents are left
// out: an agent needs a session. Throws on a file without a tokens list.
export function parseDevUsers(_yamlText: string): DevUser[] {
  return todo();
}

export function isCustomer(_u: Pick<DevUser, "principal">): boolean {
  return todo();
}

// The sign-in screen's two groups, each in the file's order.
export function groupUsers(_users: DevUser[]): { staff: DevUser[]; customers: DevUser[] } {
  return todo();
}

// What a person may open: a customer the portal only; staff the tracker,
// and the service desk when they hold desk_agent (desk first).
export function areasFor(_u: DevUser): Area[] {
  return todo();
}

// Where a person lands after signing in: their first area.
export function homeFor(_u: DevUser): string {
  return todo();
}
