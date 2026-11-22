// DEV ONLY. The people the sign-in screen offers, read from the dev-token
// file the item service, pli and web/ use (dev-tokens.example.yaml), which
// the stack's poc container serves. Never in a production build: the
// sign-in list is compiled in only with VITE_DEV_USERS=1.
//
// There is no "who am I" in the API: a person's roles come from this file.
import { parse } from "yaml";

export type DevUser = { name: string; token: string; principal: string; namespace: string; roles: string[] };

// The four areas of the app.
export type Area = "desk" | "tracker" | "portal" | "requests";

// The file's users and customers, in the file's order. Agents are left
// out: an agent needs a session. Throws on a file without a tokens list.
export function parseDevUsers(yamlText: string): DevUser[] {
  let doc: unknown;
  try {
    doc = parse(yamlText);
  } catch {
    doc = undefined;
  }
  const tokens = (doc as { tokens?: unknown } | null | undefined)?.tokens;
  if (!Array.isArray(tokens)) throw new Error("dev users: the file has no tokens list");
  return tokens
    .map((t: Record<string, unknown>) => ({
      name: String(t.name),
      token: String(t.token),
      principal: String(t.principal),
      namespace: String(t.namespace),
      roles: Array.isArray(t.roles) ? t.roles.map(String) : [],
    }))
    .filter((u) => u.principal.startsWith("user:") || u.principal.startsWith("customer:"));
}

export function isCustomer(u: Pick<DevUser, "principal">): boolean {
  return u.principal.startsWith("customer:");
}

// The sign-in screen's two groups, each in the file's order.
export function groupUsers(users: DevUser[]): { staff: DevUser[]; customers: DevUser[] } {
  return { staff: users.filter((u) => !isCustomer(u)), customers: users.filter(isCustomer) };
}

// What a person may open: a customer the portal only; staff the tracker,
// the service desk when they hold desk_agent (desk first), and Requests,
// last, for everyone on staff: the vacation request names no role for
// asking or deciding (creator, and whoever HR names as approver).
export function areasFor(u: DevUser): Area[] {
  if (isCustomer(u)) return ["portal"];
  return [...(u.roles.includes("desk_agent") ? ["desk" as const] : []), "tracker", "requests"];
}

// Requests' Balances: for those who hold hr_admin, the role leave_balance
// gives its owner and allowance to.
export function seesBalances(u: DevUser): boolean {
  return !isCustomer(u) && u.roles.includes("hr_admin");
}

// Where a person lands after signing in: their first area.
export function homeFor(u: DevUser): string {
  return `#/${areasFor(u)[0]}`;
}
