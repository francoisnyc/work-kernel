// The server's refusal, read from a Connect error: its code, and the
// google.rpc.ErrorInfo's stable reason and metadata (docs/design.md,
// "Errors"). The UI shows these; it never guesses a reason itself.
//
// Phase B: signatures only.

export type Refusal = {
  // The code as pli prints it: FAILED_PRECONDITION, PERMISSION_DENIED...
  code: string;
  // ErrorInfo.reason: MISSING_FIELDS, FIELD_FROZEN... "" when there is none.
  reason: string;
  message: string;
  // ErrorInfo.metadata: fields, state, link_type, hold...
  metadata: Record<string, string>;
  // BadRequest field violations (INVALID_ARGUMENT).
  violations: { field: string; description: string }[];
};

// Any thrown value as a Refusal. Something that is not a Connect error
// (the network is down) has code UNKNOWN and no reason.
export function refusalOf(_err: unknown): Refusal {
  throw new Error("not implemented");
}
