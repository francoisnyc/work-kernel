// The server's refusal, read from a Connect error, and said in plain
// words. The code and reason stay available for the "Details" disclosure:
// the app never hides what the server answered, and never guesses a
// refusal the server did not make.
import { Code, ConnectError } from "@connectrpc/connect";
import { BadRequestSchema, ErrorInfoSchema } from "@gen/google/rpc/error_details_pb";
import { fieldLabel, stateLabel } from "./labels";

// The ErrorInfo domain of the item service: frozen (docs/design.md).
const DOMAIN = "pli.francoisnyc.github.io";

export type Refusal = {
  // As pli prints it: FAILED_PRECONDITION, PERMISSION_DENIED... UNKNOWN for
  // anything that is not a Connect error (the network is down).
  code: string;
  // ErrorInfo.reason (MISSING_FIELDS...), "" when there is none.
  reason: string;
  message: string;
  metadata: Record<string, string>;
  violations: { field: string; description: string }[];
};

// Any thrown value as a Refusal.
export function refusalOf(err: unknown): Refusal {
  if (!(err instanceof ConnectError)) {
    const message = err instanceof Error ? err.message : String(err);
    return { code: "UNKNOWN", reason: "", message, metadata: {}, violations: [] };
  }
  const info = err.findDetails(ErrorInfoSchema).find((d) => d.domain === DOMAIN);
  return {
    // Code's names are CamelCase (FailedPrecondition).
    code: Code[err.code].replace(/([a-z])([A-Z])/g, "$1_$2").toUpperCase(),
    reason: info?.reason ?? "",
    message: err.rawMessage,
    metadata: { ...info?.metadata },
    violations: err
      .findDetails(BadRequestSchema)
      .flatMap((b) => b.fieldViolations.map((v) => ({ field: v.field, description: v.description }))),
  };
}

// What a screen may add: a sentence for a guard it knows ("An epic is done
// only when..."). Field, state and link words come from labels.ts.
export type ExplainContext = { guard?: string; typeId?: string };

const lower = (s: string) => s.charAt(0).toLowerCase() + s.slice(1);
const fieldsIn = (m: Record<string, string>) => (m.fields ?? "").split(",").filter((f) => f !== "").map(fieldLabel);
const article = (word: string) => (/^[aeiou]/i.test(word) ? `an ${word}` : `a ${word}`);

const invariants: Record<string, string> = { priority_missing: "Choose a priority." };

const fixed: Record<string, string> = {
  STALE: "Someone changed this since you opened it. Reload to see their change, then try again.",
  NOT_ALLOWED: "You don't have permission to do that.",
  NOT_FOUND: "This doesn't exist, or you can't see it.",
  ALREADY_LINKED: "These are already linked.",
  NOT_LINKED: "These aren't linked.",
  HELD: "This is waiting on a check. Try again in a moment.",
  GUARD_FAILED: "The rules for this move aren't met yet.",
  UNAUTHENTICATED: "Your sign-in is no longer valid. Sign in again.",
  NOT_CAUGHT_UP: "The server is catching up. Try again in a moment.",
  INVALID_REQUEST: "Some values aren't valid. See the details.",
  IDEMPOTENCY_KEY_REUSED: "This was already sent. Reload to see where it stands.",
  LINK_NOT_WRITABLE: "You can't set this kind of link.",
  LINK_TARGET_TYPE: "That kind of item can't be linked here.",
};

// One plain sentence for a refusal.
export function explain(r: Refusal, ctx: ExplainContext = {}): string {
  const m = r.metadata;
  switch (r.reason) {
    case "MISSING_FIELDS":
      return `Fill in ${fieldsIn(m).map(lower).join(" and ")} first.`;
    case "FIELD_FROZEN":
      return `${fieldsIn(m).join(" and ")} can't be changed while this is ${stateLabel(ctx.typeId ?? "", m.state ?? "")}.`;
    case "FIELD_NOT_WRITABLE":
      return `You can't change ${fieldsIn(m).map(lower).join(" and ")}.`;
    case "INVARIANT_FAILED":
      return invariants[m.invariant ?? ""] ?? "That would break one of this item's rules.";
    case "LINK_CARDINALITY":
      return `This already has ${article(lower(fieldLabel(m.link_type ?? "link")))}. Remove that one first.`;
    case "GUARD_FAILED":
      return ctx.guard ?? fixed.GUARD_FAILED!;
    case "":
      if (r.code === "UNKNOWN") return "Can't reach the server. Check your connection and try again.";
      return `The server refused this (${r.code}).`;
  }
  return fixed[r.reason] ?? `The server refused this (${r.reason}).`;
}
