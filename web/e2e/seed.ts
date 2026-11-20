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

// --- Jira part 2, step 3b: an incident in a desk, with its goals ---

async function until<T>(what: string, read: () => Promise<T | undefined>, seconds = 30): Promise<T> {
  for (let i = 0; i < seconds * 4; i++) {
    const got = await read();
    if (got !== undefined) return got;
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`seed: ${what}: not within ${seconds} s`);
}

export type Desk = { desk: Item; incident: Item; firstResponse: Item; resolution: Item; bug: Item };

// lea's IT desk (she holds sla_admin: only she links its calendar and its
// policy), ana's P3 incident in it, assigned to sam, and a bug of sam's
// linked to the incident by caused_by. The worker creates the two goals;
// this waits for them and for the due instants they show on the incident.
// The P3 targets are hours: nothing breaches while the test runs.
export async function seedIncident(baseURL: string, run: string): Promise<Desk> {
  const lea = api(baseURL, "lea");
  const ana = api(baseURL, "ana");
  const sam = api(baseURL, "sam");
  const week = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"].map((d) => `${d} 00:00-24:00`);
  const calendar = await createItem(lea, type("calendar", 1), { f_name: `Always ${run}`, f_week: week, f_zone: "UTC" });
  const targets: JsonObject = { f_name: `Standard ${run}` };
  ["p1", "p2", "p3", "p4"].forEach((p, i) => {
    targets[`f_first_response_s_${p}`] = String(3600 * (i + 1));
    targets[`f_resolution_s_${p}`] = String(4 * 3600 * (i + 1)); // P3: 12 hours
  });
  const policy = await createItem(lea, type("sla_policy", 1), targets);
  const desk = await createItem(lea, type("desk", 1), { f_key: `IT${run}`, f_name: `IT desk ${run}` });
  await lea.linkItems({ name: desk.name, requestId: randomUUID(), linkType: "calendar", target: calendar.name });
  await lea.linkItems({ name: desk.name, requestId: randomUUID(), linkType: "sla_policy", target: policy.name });

  const incident = await createItem(
    ana,
    type("incident", 1),
    { f_title: `VPN down ${run}`, f_priority: "P3", f_assignee: "user:sam" },
    { container: desk.name },
  );
  const goal = (metric: string) =>
    until(`the incident's ${metric} goal`, async () => {
      const res = await ana.listItems({ parent: incident.name, pageSize: 50 });
      return res.items.find((i) => i.type === type("sla_goal", 1) && i.fields?.f_metric === metric);
    });
  const firstResponse = await goal("first_response");
  const resolution = await goal("resolution");
  await until("both due instants on the incident", async () => {
    const got = (await ana.getItem({ name: incident.name })).item;
    return got?.fields?.f_due_resolution && got.fields.f_due_first_response ? got : undefined;
  });

  const space = await createItem(sam, type("space", 1), { f_key: `B${run}`, f_name: `Bugs ${run}` });
  const bug = await createItem(sam, type("bug", 3), { f_summary: `Gateway leaks memory ${run}` }, { container: space.name });
  await sam.linkItems({ name: incident.name, requestId: randomUUID(), linkType: "caused_by", target: bug.name });
  return { desk, incident, firstResponse, resolution, bug };
}
