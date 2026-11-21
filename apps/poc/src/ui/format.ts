// Times as people read them, on this browser's clock and in its language.
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { parseInstant } from "../model/countdown";

export function when(at: string | Timestamp | undefined): string {
  if (at === undefined) return "";
  const ms = typeof at === "string" ? parseInstant(at) : timestampDate(at).getTime();
  if (Number.isNaN(ms)) return "";
  return new Date(ms).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export function isoOf(at: Timestamp | undefined): string {
  return at ? timestampDate(at).toISOString() : "";
}
