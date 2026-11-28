// @vitest-environment node
// The Inbox, the look: the words the model gives the screen (the brief,
// docs/plans/inbox-the-look.md, items 2, 4 and 6, and the owner's answers
// at Phase A, 2026-11-28). Pure functions.
//
//   - typeLabel: a Type by its own label (Type.label); when the Item's
//     version has none, the label of the newest version of the same Type
//     ID that has one (a label names the Type, not the version: the seeds
//     and reactions still create older versions); its ID otherwise.
//   - Agents by name: an agent's `display_name` from the dev-token file
//     (parseDevAgents), the ID without "agent:" otherwise; the people
//     picker still leaves agents out.
import { create } from "@bufbuild/protobuf";
import { readFileSync } from "node:fs";
import { describe, expect, test } from "vitest";
import { StateCategory, TypeSchema, type Type } from "../gen/kernel/v1/kernel_pb";
import { item, itemName, typeName } from "../test/fixtures";
import { parseDevAgents, parseDevUsers, type DevAgent, type DevUser } from "./devusers";
import { agentLine, agentMark } from "./inbox";
import { personName } from "./people";
import { typeLabel } from "./typeinfo";

const version = (id: string, v: number, label = ""): Type =>
  create(TypeSchema, {
    name: typeName(id, v),
    typeId: id,
    version: v,
    label,
    states: [{ id: "open", category: StateCategory.TODO }],
  });

describe("typeLabel: a Type by its label", () => {
  const types = [
    version("approval", 1),
    version("approval", 2),
    version("approval", 3, "Approval"),
    version("incident", 1),
    version("incident", 2, "Old name"),
    version("incident", 3),
    version("purchase", 1),
    version("purchase", 2),
  ];
  const of = (id: string, v: number) => types.find((t) => t.typeId === id && t.version === v);

  test("its own label", () => {
    expect(typeLabel(of("approval", 3), types)).toBe("Approval");
  });

  test("an older version without one: the label of the newest version of the same Type that has one", () => {
    expect(typeLabel(of("approval", 1), types)).toBe("Approval");
    expect(typeLabel(of("approval", 2), types)).toBe("Approval");
    // The newest that has one, even if a newer one has none.
    expect(typeLabel(of("incident", 1), types)).toBe("Old name");
    expect(typeLabel(of("incident", 3), types)).toBe("Old name");
  });

  test("no version of the Type has one: its ID", () => {
    expect(typeLabel(of("purchase", 1), types)).toBe("purchase");
  });

  test("another Type's label is never used", () => {
    expect(typeLabel(version("approval_note", 1), types)).toBe("approval_note");
  });

  test("a Type the page does not have: the ID from the Item's type name, when given", () => {
    expect(typeLabel(undefined, types, typeName("approval", 9))).toBe("Approval");
    expect(typeLabel(undefined, types, typeName("ghost", 1))).toBe("ghost");
    expect(typeLabel(undefined, types)).toBe("");
  });
});

// The real file the stack mounts.
const file = readFileSync(new URL("../../../dev-tokens.example.yaml", import.meta.url), "utf8");

describe("agents by name, from the dev-token file", () => {
  test("parseDevAgents: the file's agents, in its order, with their display names", () => {
    expect(parseDevAgents(file)).toEqual([
      { name: "triage-bot", principal: "agent:triage-bot", namespace: "acme", displayName: "Triage agent" },
      { name: "portal-bot", principal: "agent:portal-bot", namespace: "acme", displayName: "Portal agent" },
    ]);
  });

  test("an agent without display_name: an empty one", () => {
    const agents = parseDevAgents(`tokens:
  - {name: ana, token: a, principal: "user:ana", namespace: acme}
  - {name: sweeper, token: s, principal: "agent:sweeper", namespace: acme}
`);
    expect(agents).toEqual([{ name: "sweeper", principal: "agent:sweeper", namespace: "acme", displayName: "" }]);
  });

  test("the people picker still leaves agents out", () => {
    expect(parseDevUsers(file).some((u) => u.principal.startsWith("agent:"))).toBe(false);
  });

  const people: DevUser[] = [{ name: "ana", token: "a", principal: "user:ana", namespace: "acme", roles: [] }];
  const agents: DevAgent[] = [
    { name: "triage-bot", principal: "agent:triage-bot", namespace: "acme", displayName: "Triage agent" },
    { name: "sweeper", principal: "agent:sweeper", namespace: "acme", displayName: "" },
  ];
  const nameOf = (p: string) => personName(p, people, agents);

  test("personName: an agent's display name; its ID without one; people as before", () => {
    expect(nameOf("agent:triage-bot")).toBe("Triage agent");
    expect(nameOf("agent:sweeper")).toBe("agent:sweeper");
    expect(nameOf("agent:unknown")).toBe("agent:unknown");
    expect(nameOf("user:ana")).toBe("Ana");
  });

  test("an agent's request names the agent by its display name, else its ID without agent:", () => {
    const row = (stateActor: string) =>
      item({ name: itemName(1), type: typeName("ticket", 9), state: "open", creator: "user:ana", stateActor, stateActorOnBehalfOf: "user:ana" });
    const named = agentMark(row("agent:triage-bot"), "user:bob", nameOf)!;
    expect(named).toEqual({ agent: "Triage agent", forWhom: "Ana" });
    expect(agentLine(named)).toBe("Triage agent, for Ana");
    expect(agentMark(row("agent:sweeper"), "user:bob", nameOf)).toEqual({ agent: "sweeper", forWhom: "Ana" });
  });
});
