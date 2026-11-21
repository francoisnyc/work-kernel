// The server's refusal, read from a Connect error, and said in plain
// words. The code and reason stay available for the "Details" disclosure:
// the app never hides what the server answered, and never guesses a
// refusal the server did not make.
import { todo } from "./todo";

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
export function refusalOf(_err: unknown): Refusal {
  return todo();
}

// What a screen may add: a sentence for a guard it knows ("An epic is done
// only when..."). Field, state and link words come from labels.ts.
export type ExplainContext = { guard?: string; typeId?: string };

// One plain sentence for a refusal.
export function explain(_r: Refusal, _ctx?: ExplainContext): string {
  return todo();
}
