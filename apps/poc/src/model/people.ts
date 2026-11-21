// How the apps name people. The API has no directory and no display names:
// a principal is shown by its ID.

export type Audience = "staff" | "customer";

// "user:sam" -> "sam"; "customer:kim" -> "kim"; "agent:triage-bot" ->
// "triage-bot (agent)"; the kernel's own actors (system:..., reaction:...,
// hook:...) -> "System".
export function personName(principal: string): string {
  const i = principal.indexOf(":");
  if (i < 0) return principal === "" ? "Someone" : principal;
  const kind = principal.slice(0, i);
  const id = principal.slice(i + 1);
  if (kind === "user" || kind === "customer") return id;
  if (kind === "agent") return `${id} (agent)`;
  return "System";
}

// One or two capital letters for an avatar: "sam" -> "S", "triage-bot" -> "TB".
export function initials(principal: string): string {
  const id = principal.slice(principal.indexOf(":") + 1);
  const parts = id.split(/[-_. ]+/).filter((p) => p !== "");
  return parts
    .slice(0, 2)
    .map((p) => p[0]!.toUpperCase())
    .join("");
}

// The author of a message as the viewer sees it: "You" for the viewer;
// to a customer, staff are "sam · Support"; to staff, a customer is
// "kim · Customer"; anyone else by personName.
export function authorLabel(principal: string, viewer: string, audience: Audience): string {
  if (principal === viewer) return "You";
  const name = personName(principal);
  if (audience === "customer" && principal.startsWith("user:")) return `${name} · Support`;
  if (audience === "staff" && principal.startsWith("customer:")) return `${name} · Customer`;
  return name;
}
