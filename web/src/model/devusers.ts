// DEV ONLY. The dev users the "act as" picker offers, read from the
// dev-token file the item service and pli use (dev-tokens.example.yaml),
// which the compose stack's web container serves. Never in a production
// build: the picker is compiled in only with VITE_DEV_USERS=1.
import { parse } from "yaml";

export type DevUser = { name: string; token: string; principal: string; namespace: string; roles: string[] };

// The file's users and customers, in the file's order. Agents are left
// out: an agent needs a session, which the UI does not start. Throws on a
// file without a tokens list.
export function parseDevUsers(yamlText: string): DevUser[] {
  return tokensOf(yamlText)
    .map((t) => ({
      name: String(t.name),
      token: String(t.token),
      principal: String(t.principal),
      namespace: String(t.namespace),
      roles: Array.isArray(t.roles) ? t.roles.map(String) : [],
    }))
    .filter((u) => u.principal.startsWith("user:") || u.principal.startsWith("customer:"));
}

// The file's token entries. Throws on a file without a tokens list.
function tokensOf(yamlText: string): Record<string, unknown>[] {
  let doc: unknown;
  try {
    doc = parse(yamlText);
  } catch {
    doc = undefined;
  }
  const tokens = (doc as { tokens?: unknown } | null | undefined)?.tokens;
  if (!Array.isArray(tokens)) throw new Error("dev users: the file has no tokens list");
  return tokens as Record<string, unknown>[];
}

// A person's name from the dev-token file (people.ts, which has no YAML
// in it, so the session can use it in a production build).
export { personName } from "./people";

// An agent of the dev-token file, for its name on screens (the Inbox, the
// look): `display_name`, "" when the file gives none.
export type DevAgent = { name: string; principal: string; namespace: string; displayName: string };

// The file's agents, in the file's order. Throws on a file without a
// tokens list, as parseDevUsers does.
export function parseDevAgents(yamlText: string): DevAgent[] {
  return tokensOf(yamlText)
    .filter((t) => String(t.principal).startsWith("agent:"))
    .map((t) => ({
      name: String(t.name),
      principal: String(t.principal),
      namespace: String(t.namespace),
      displayName: typeof t.display_name === "string" ? t.display_name : "",
    }));
}
