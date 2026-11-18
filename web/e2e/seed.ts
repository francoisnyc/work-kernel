// What the smoke test needs before the browser opens, made through the
// public API (through the web container's /api proxy, so the proxy is
// tested too): never through the database. The three behaviours under
// test are done in the browser, not here.
import { create, type JsonObject } from "@bufbuild/protobuf";
import { createClient, type Client, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { DurationSchema } from "@bufbuild/protobuf/wkt";
import { ItemService, LinkDirection, TransitionOutcome, type Item } from "../src/gen/kernel/v1/kernel_pb";
import { parseDevUsers } from "../src/model/devusers";
import { evenly } from "../src/rank/rank";

const users = () => parseDevUsers(readFileSync(new URL("../../dev-tokens.example.yaml", import.meta.url), "utf8"));

export type Api = Client<typeof ItemService>;

// A client that calls as one dev user.
export function api(baseURL: string, name: string): Api {
  const user = users().find((u) => u.name === name);
  if (!user) throw new Error(`no dev user ${name}`);
  const auth: Interceptor = (next) => (req) => {
    req.header.set("Authorization", `Bearer ${user.token}`);
    return next(req);
  };
  return createClient(ItemService, createConnectTransport({ baseUrl: `${baseURL}/api`, interceptors: [auth] }));
}

const NS = "namespaces/acme";
const type = (id: string, version: number) => `${NS}/types/${id}/versions/${version}`;

async function createItem(as: Api, typeName: string, fields: JsonObject, opts: { container?: string; narrow?: string[] } = {}) {
  const res = await as.createItem({
    parent: NS,
    requestId: randomUUID(),
    type: typeName,
    fields,
    container: opts.container ?? "",
    narrow: opts.narrow ? { principals: opts.narrow } : undefined,
  });
  return res.item!;
}

// A date some days from today, as the vacation request's guard wants
// (f_start >= today).
function inDays(n: number): string {
  return new Date(Date.now() + n * 86_400_000).toISOString().slice(0, 10);
}

// ana's vacation request, submitted, waiting for bob (the HR directory
// says ana reports to bob). hana first closes ana's open leave balances
// and opens one, as scripts/scenario.sh does: a request finds its balance
// only when exactly one is open.
export async function seedVacation(baseURL: string, comment: string): Promise<Item> {
  const hana = api(baseURL, "hana");
  const ana = api(baseURL, "ana");
  const open = await hana.searchItems({
    parent: NS,
    pageSize: 100,
    filter: {
      types: [`${NS}/types/leave_balance`],
      states: ["open"],
      fields: [{ field: "f_owner", eq: { kind: { case: "stringValue", value: "user:ana" } } }],
    },
  });
  for (const b of open.items) {
    await hana.transitionItem({ name: b.name, requestId: randomUUID(), toState: "closed" });
  }
  await createItem(hana, type("leave_balance", 1), { f_owner: "user:ana", f_allowance: "25" }, { narrow: ["user:ana", "user:hana"] });

  const request = await createItem(ana, type("vacation_request", 3), {
    f_start: inDays(10),
    f_end: inDays(11),
    f_days: "2",
    f_leave_type: "annual",
    f_comment: comment,
  });
  const submitted = await ana.transitionItem({
    name: request.name,
    requestId: randomUUID(),
    toState: "submitted",
    wait: create(DurationSchema, { seconds: 5n }),
  });
  if (submitted.outcome !== TransitionOutcome.APPLIED) {
    throw new Error(`seed: submitting the vacation request: ${TransitionOutcome[submitted.outcome]} ${submitted.reason}`);
  }
  return submitted.item!;
}

export type Project = { space: Item; sprint: Item; stories: Item[]; outsider: Item };

// A PLAT-like project by lea: a space, a sprint, three stories linked to
// the sprint, and a fourth story that is not. The space's rank order is
// story 1, the outsider, story 2, story 3: the board shows 1, 2, 3.
export async function seedProject(baseURL: string, run: string): Promise<Project> {
  const lea = api(baseURL, "lea");
  const space = await createItem(lea, type("space", 1), { f_key: `S${run}`, f_name: `Smoke ${run}` });
  const sprint = await createItem(lea, type("sprint", 1), { f_name: `Sprint ${run}` }, { container: space.name });
  const keys = evenly("", "", 4);
  const story = (title: string, key: string) =>
    createItem(lea, type("story", 1), { f_summary: title, f_rank: key }, { container: space.name });
  const one = await story(`Story one ${run}`, keys[0]!);
  const outsider = await story(`Outsider ${run}`, keys[1]!);
  const two = await story(`Story two ${run}`, keys[2]!);
  const three = await story(`Story three ${run}`, keys[3]!);
  for (const s of [one, two, three]) {
    await lea.linkItems({ name: s.name, requestId: randomUUID(), linkType: "sprint", target: sprint.name });
  }
  return { space, sprint, stories: [one, two, three], outsider };
}

// The space's stories in rank order, as lea sees them: the container's
// order, which a move on the sprint's board must keep.
export async function containerOrder(baseURL: string, space: Item): Promise<string[]> {
  const res = await api(baseURL, "lea").listItems({ parent: space.name, orderBy: "f_rank", pageSize: 100 });
  return res.items.filter((i) => i.fields?.f_rank).map((i) => String(i.fields?.f_summary));
}

// The sprint's board through the API: what a reload must show.
export async function boardOrder(baseURL: string, sprint: Item): Promise<string[]> {
  const res = await api(baseURL, "lea").searchItems({
    parent: NS,
    orderBy: "f_rank",
    pageSize: 100,
    filter: { links: [{ linkType: "sprint", direction: LinkDirection.OUTGOING, item: sprint.name }] },
  });
  return res.items.map((i) => String(i.fields?.f_summary));
}

export const idOf = (item: Item) => item.name.split("/").pop()!;
