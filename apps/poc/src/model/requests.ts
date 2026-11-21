// The portal's writes and the desk's messages.
import type { Item } from "@gen/kernel/v1/kernel_pb";
import type { Api } from "../app/api";
import { todo } from "./todo";

export type NewRequest = { title: string; urgency: string; impact: string; description: string };

// The form's problems, by field ("title", "description"); {} when it can
// be sent.
export function validateRequest(_form: NewRequest): Partial<Record<keyof NewRequest, string>> {
  return todo();
}

// Opens a request in a desk: the incident (title, priority, impact when
// chosen), then its description as the requester's first reply (incident@2
// has no description field). If that second write fails the request still
// exists: detailsSaved is false and the page offers to add them.
export async function openRequest(
  _api: Pick<Api, "createItem">,
  _ns: string,
  _desk: string,
  _form: NewRequest,
): Promise<{ request: Item; detailsSaved: boolean }> {
  return todo();
}

// A reply (public, reply@1) or a note (internal, note@1) in an incident.
export async function postMessage(
  _api: Pick<Api, "createItem">,
  _ns: string,
  _incident: string,
  _kind: "reply" | "note",
  _body: string,
): Promise<Item> {
  return todo();
}
