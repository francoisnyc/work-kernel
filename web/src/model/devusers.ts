// DEV ONLY. The dev users the "act as" picker offers, read from the
// dev-token file the item service and pli use (dev-tokens.example.yaml),
// which the compose stack's web container serves. Never in a production
// build: the picker is compiled in only with VITE_DEV_USERS=1.
//
// Phase B: signatures only.

export type DevUser = { name: string; token: string; principal: string; namespace: string; roles: string[] };

// The file's users, in the file's order. Agents are left out: an agent
// needs a session, which the UI does not start. Throws on a file without
// a tokens list.
export function parseDevUsers(_yamlText: string): DevUser[] {
  throw new Error("not implemented");
}
