// People's names for the page (the Inbox for people, PR 2b). The kernel
// has no display names (design.md, "PoC apps as built", gap 1): a name
// comes from the dev-token file where the page has it, else the ID.
import type { DevAgent, DevUser } from "./devusers";

// The dev-token file's `name`, capitalised ("sam" is "Sam"); the
// principal's ID when the file does not have them.
// agents: the file's agents, for their display names (the Inbox, the look;
// STUB for the Phase B tests: not read yet).
export function personName(principal: string, people: readonly DevUser[], _agents: readonly DevAgent[] = []): string {
  const name = people.find((p) => p.principal === principal)?.name;
  return name ? name.charAt(0).toUpperCase() + name.slice(1) : principal;
}
