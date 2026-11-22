// The portal's request: the incident, then its description as the first
// reply. The desk's messages: a reply or a note, never the other.
import { describe, expect, test, vi } from "vitest";
import type { Api } from "../app/api";
import { NS, item, itemName, typeName } from "../test/fixtures";
import { openRequest, postMessage, validateRequest, type NewRequest } from "./requests";

const desk = itemName("desk");
const form: NewRequest = { title: "  Printer jam  ", urgency: "P2", impact: "", description: "It eats paper." };

function fakeApi(fail?: number) {
  let n = 0;
  type Req = { type: string; container: string; requestId: string; fields: Record<string, unknown> };
  const createItem = vi.fn(async (req: Req) => {
    n++;
    if (n === fail) throw new Error("boom");
    return { item: item(`created-${n}`, req.type, { container: req.container }), itemSeq: 1n, commitSeq: 0n };
  });
  return { api: { createItem } as unknown as Pick<Api, "createItem">, createItem };
}

describe("validateRequest", () => {
  test("a title and a description are needed", () => {
    expect(validateRequest({ title: " ", urgency: "P3", impact: "", description: "" })).toEqual({
      title: "Give your request a title.",
      description: "Tell us what's happening.",
    });
    expect(validateRequest(form)).toEqual({});
  });
});

describe("openRequest", () => {
  test("creates the incident in the desk, then the description as a reply inside it", async () => {
    const { api, createItem } = fakeApi();
    const out = await openRequest(api, NS, desk, form);
    expect(createItem).toHaveBeenCalledTimes(2);
    const [first] = createItem.mock.calls[0]!;
    const [second] = createItem.mock.calls[1]!;
    expect(first).toMatchObject({
      parent: NS,
      type: typeName("incident", 3),
      container: desk,
      fields: { f_title: "Printer jam", f_priority: "P2" },
    });
    // No impact chosen: not sent.
    expect(first.fields).not.toHaveProperty("f_impact");
    expect(second).toMatchObject({
      parent: NS,
      type: typeName("reply", 2),
      container: out.request.name,
      fields: { f_body: "It eats paper." },
    });
    expect(first.requestId).not.toBe(second.requestId);
    expect(out.detailsSaved).toBe(true);
  });

  test("sends the impact when chosen", async () => {
    const { api, createItem } = fakeApi();
    await openRequest(api, NS, desk, { ...form, impact: "minor" });
    expect(createItem.mock.calls[0]![0]).toMatchObject({ fields: { f_impact: "minor" } });
  });

  test("the request stands when its description could not be saved", async () => {
    const { api } = fakeApi(2);
    const out = await openRequest(api, NS, desk, form);
    expect(out.request.type).toBe(typeName("incident", 3));
    expect(out.detailsSaved).toBe(false);
  });

  test("a refused incident is thrown, and nothing else is written", async () => {
    const { api, createItem } = fakeApi(1);
    await expect(openRequest(api, NS, desk, form)).rejects.toThrow("boom");
    expect(createItem).toHaveBeenCalledTimes(1);
  });
});

describe("postMessage", () => {
  test("a reply is a reply@2 and a note a note@2, inside the incident", async () => {
    const { api, createItem } = fakeApi();
    const inc = itemName("inc");
    await postMessage(api, NS, inc, "reply", "We are on it.");
    await postMessage(api, NS, inc, "note", "Check the logs.");
    expect(createItem.mock.calls[0]![0]).toMatchObject({ type: typeName("reply", 2), container: inc, fields: { f_body: "We are on it." } });
    expect(createItem.mock.calls[1]![0]).toMatchObject({ type: typeName("note", 2), container: inc, fields: { f_body: "Check the logs." } });
  });
});
