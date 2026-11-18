// The server's refusal, read from a Connect error: its code, and the
// google.rpc.ErrorInfo's stable reason and metadata (docs/design.md,
// "Errors"). The UI shows these; it never guesses a reason itself.
import { Code, ConnectError } from "@connectrpc/connect";
import { BadRequestSchema, ErrorInfoSchema } from "../gen/google/rpc/error_details_pb";

// The ErrorInfo domain of the item service: frozen (docs/design.md).
const DOMAIN = "pli.francoisnyc.github.io";

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
