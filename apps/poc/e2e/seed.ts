// What the journeys need before the browser opens, made through the public
// API (through the poc container's /api proxy), never through the
// database. Every name carries the run's word, so the journeys run any
// number of times on one stack, after make scenario or not.
import { evenly } from "@rank/rank";
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { parse } from "yaml";

export const NS = "namespaces/acme";
export const type = (id: string, version: number) => `${NS}/types/${id}/versions/${version}`;

// An Item as the API's JSON has it (only what the journeys read).
export type Item = { name: string; type: string; state: string; container?: string; fields?: Record<string, unknown> };
export const idOf = (item: Item) => item.name.split("/").pop()!;

// The dev-token file's tokens, read here rather than through the app's own
// parser: the seed must not depend on the code under test.
const tokens = (parse(readFileSync(new URL("../../../dev-tokens.example.yaml", import.meta.url), "utf8")) as {
  tokens: { name: string; token: string }[];
}).tokens;

// The public API as one dev user, in the Connect protocol with JSON
// bodies, over plain fetch: what any client can do, with no generated code
// (the journeys do not load web/'s generated client, whose own imports
// resolve from web/).
export type Api = (method: string, body: Record<string, unknown>) => Promise<Record<string, any>>;

export function api(baseURL: string, name: string): Api {
  const t = tokens.find((x) => x.name === name);
  if (!t) throw new Error(`no dev user ${name}`);
  return async (method, body) => {
    const res = await fetch(`${baseURL}/api/kernel.v1.ItemService/${method}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${t.token}` },
      body: JSON.stringify(body),
    });
    const json = (await res.json()) as Record<string, any>;
    if (!res.ok) throw new Error(`seed: ${name} ${method}: ${json.code} ${json.message}`);
    return json;
  };
}

// Every write carries a fresh request_id.
const write = (as: Api, method: string, body: Record<string, unknown>) => as(method, { requestId: randomUUID(), ...body });

export async function createItem(as: Api, typeName: string, fields: Record<string, unknown>, container = ""): Promise<Item> {
  return (await write(as, "CreateItem", { parent: NS, type: typeName, fields, container })).item as Item;
}
export const getItem = async (as: Api, name: string): Promise<Item> => (await as("GetItem", { name })).item as Item;
export const transition = (as: Api, name: string, toState: string, fields?: Record<string, unknown>) =>
  write(as, "TransitionItem", { name, toState, fields });
export const update = (as: Api, name: string, fields: Record<string, unknown>) => write(as, "UpdateItem", { name, fields });
export const link = (as: Api, name: string, linkType: string, target: string) => write(as, "LinkItems", { name, linkType, target });

export async function until<T>(what: string, read: () => Promise<T | undefined>, seconds = 20): Promise<T> {
  for (let i = 0; i < seconds * 4; i++) {
    const got = await read();
    if (got !== undefined) return got;
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`seed: ${what}: not within ${seconds} s`);
}

// --- The service desk ---

// Seconds of working time per priority, P1 to P4.
export type Targets = { firstResponse: [number, number, number, number]; resolution: [number, number, number, number] };

export const hours: Targets = {
  firstResponse: [3600, 2 * 3600, 3 * 3600, 4 * 3600],
  resolution: [2 * 3600, 3 * 3600, 4 * 3600, 8 * 3600],
};

// lea's portal (desk@2): she holds sla_admin, so only she links its
// calendar (always open) and its policy. Customers see it by its name.
export async function seedDesk(baseURL: string, run: string, name: string, targets: Targets = hours): Promise<Item> {
  const lea = api(baseURL, "lea");
  const week = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"].map((d) => `${d} 00:00-24:00`);
  const calendar = await createItem(lea, type("calendar", 1), { f_name: `Always ${run}`, f_week: week, f_zone: "UTC" });
  const policy: Record<string, unknown> = { f_name: `Policy ${run}` };
  ["p1", "p2", "p3", "p4"].forEach((p, i) => {
    policy[`f_first_response_s_${p}`] = String(targets.firstResponse[i]);
    policy[`f_resolution_s_${p}`] = String(targets.resolution[i]);
  });
  const pol = await createItem(lea, type("sla_policy", 1), policy);
  const desk = await createItem(lea, type("desk", 2), { f_name: name, f_key: `E2E-${run}` });
  await link(lea, desk.name, "calendar", calendar.name);
  await link(lea, desk.name, "sla_policy", pol.name);
  return desk;
}

// A customer's request in a desk, as the portal opens one: the incident,
// then its description as her first reply. Waits, as staff, for the two
// due instants its goals copy onto it.
export async function seedRequest(baseURL: string, customer: string, desk: Item, title: string, priority: string): Promise<Item> {
  const as = api(baseURL, customer);
  const incident = await createItem(as, type("incident", 2), { f_title: title, f_priority: priority }, desk.name);
  await createItem(as, type("reply", 1), { f_body: `About ${title}: please help.` }, incident.name);
  return dueInstants(baseURL, incident);
}

// The incident as staff see it once both goals show their due instants.
export function dueInstants(baseURL: string, incident: Item): Promise<Item> {
  const lea = api(baseURL, "lea");
  return until("both due instants on the incident", async () => {
    const got = await getItem(lea, incident.name);
    return got.fields?.f_due_resolution && got.fields.f_due_first_response ? got : undefined;
  });
}

// --- The tracker ---

export type Project = { space: Item; sprint: Item; epic: Item; issues: Record<"A" | "B" | "C" | "D", Item> };

// lea's space with an active sprint (it ends in two weeks: its timer does
// not close it during the test) and an epic. A, B and C are to do and D in
// progress; all four are in the sprint and the epic, ranked A, B, C, D in
// the space.
export async function seedProject(baseURL: string, run: string): Promise<Project> {
  const lea = api(baseURL, "lea");
  const space = await createItem(lea, type("space", 1), { f_key: `E${run}`, f_name: `E2E space ${run}` });
  const ends = new Date(Date.now() + 14 * 86_400_000).toISOString();
  const sprint = await createItem(lea, type("sprint", 2), { f_name: `Sprint ${run}`, f_end_at: ends, f_goal: "Ship sign-on" }, space.name);
  await transition(lea, sprint.name, "active");
  const epic = await createItem(lea, type("epic", 1), { f_summary: `Sign-on ${run}` }, space.name);
  const keys = evenly("", "", 4);
  const mk = async (id: string, typeId: string, i: number) =>
    createItem(lea, type(typeId, 3), { f_summary: `Card ${id} ${run}`, f_priority: "P3", f_rank: keys[i]! }, space.name);
  const issues = { A: await mk("A", "story", 0), B: await mk("B", "bug", 1), C: await mk("C", "task", 2), D: await mk("D", "story", 3) };
  for (const it of Object.values(issues)) {
    await link(lea, it.name, "sprint", sprint.name);
    await link(lea, it.name, "epic", epic.name);
  }
  await transition(lea, issues.D.name, "in_progress");
  return { space, sprint, epic, issues };
}
