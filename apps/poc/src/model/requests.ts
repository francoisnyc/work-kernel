// The portal's writes and the desk's messages.
import type { Item } from "@gen/kernel/v1/kernel_pb";
import type { Api } from "../app/api";

export type NewRequest = { title: string; urgency: string; impact: string; description: string };

// The form's problems, by field ("title", "description"); {} when it can
// be sent.
export function validateRequest(form: NewRequest): Partial<Record<keyof NewRequest, string>> {
  const out: Partial<Record<keyof NewRequest, string>> = {};
  if (form.title.trim() === "") out.title = "Give your request a title.";
  if (form.description.trim() === "") out.description = "Tell us what's happening.";
  return out;
}

// Opens a request in a desk: the incident (title, priority, impact when
// chosen), then its description as the requester's first reply (incident@3
// has no description field). If that second write fails the request still
// exists: detailsSaved is false and the page offers to add them.
//
// A customer's write answers no commit_seq to keep: nothing here reads it.
export async function openRequest(
  api: Pick<Api, "createItem">,
  ns: string,
  desk: string,
  form: NewRequest,
): Promise<{ request: Item; detailsSaved: boolean }> {
  const fields: Record<string, string> = { f_title: form.title.trim(), f_priority: form.urgency };
  if (form.impact !== "") fields.f_impact = form.impact;
  const created = await api.createItem({
    parent: ns,
    requestId: crypto.randomUUID(),
    type: `${ns}/types/incident/versions/3`,
    container: desk,
    fields,
  });
  const request = created.item!;
  if (form.description.trim() === "") return { request, detailsSaved: true };
  try {
    await api.createItem({
      parent: ns,
      requestId: crypto.randomUUID(),
      type: `${ns}/types/reply/versions/2`,
      container: request.name,
      fields: { f_body: form.description.trim() },
    });
    return { request, detailsSaved: true };
  } catch {
    return { request, detailsSaved: false };
  }
}

// A reply (public, reply@2) or a note (internal, note@2) in an incident:
// both stay in the incident they were written in (placed by the kernel).
export async function postMessage(
  api: Pick<Api, "createItem">,
  ns: string,
  incident: string,
  kind: "reply" | "note",
  body: string,
): Promise<Item> {
  const res = await api.createItem({
    parent: ns,
    requestId: crypto.randomUUID(),
    type: `${ns}/types/${kind}/versions/2`,
    container: incident,
    fields: { f_body: body },
  });
  return res.item!;
}
