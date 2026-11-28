// People's names for the page (the Inbox for people, PR 2b). The kernel
// has no display names (design.md, "PoC apps as built", gap 1): a name
// comes from the dev-token file where the page has it, else the ID.
import type { DevAgent, DevUser } from "./devusers";

// The dev-token file's `name`, capitalised ("sam" is "Sam"); for an agent,
// its `display_name` (the Inbox, the look); the principal's ID when the
// file does not have them.
export function personName(principal: string, people: readonly DevUser[], agents: readonly DevAgent[] = []): string {
  const agent = agents.find((a) => a.principal === principal)?.displayName;
  if (agent) return agent;
  const name = people.find((p) => p.principal === principal)?.name;
  return name ? name.charAt(0).toUpperCase() + name.slice(1) : principal;
}
