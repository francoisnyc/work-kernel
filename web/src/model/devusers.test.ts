// @vitest-environment node
import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { parseDevUsers } from "./devusers";

// The real file the stack mounts: the picker must read exactly this.
const file = readFileSync(new URL("../../../dev-tokens.example.yaml", import.meta.url), "utf8");

test("reads the users of the real dev-token file, in its order", () => {
  const users = parseDevUsers(file);
  expect(users.map((u) => u.name)).toEqual(["ana", "bob", "dana", "erin", "hana", "lea", "sam", "carol"]);
});

test("a user carries its token, principal, namespace and roles", () => {
  const users = parseDevUsers(file);
  expect(users.find((u) => u.name === "bob")).toEqual({
    name: "bob",
    token: "dev-bob-not-a-secret",
    principal: "user:bob",
    namespace: "acme",
    roles: ["approver"],
  });
  expect(users.find((u) => u.name === "ana")?.roles).toEqual([]);
  expect(users.find((u) => u.name === "carol")?.namespace).toBe("globex");
});

test("leaves agents out: they need a session", () => {
  expect(parseDevUsers(file).some((u) => u.principal.startsWith("agent:"))).toBe(false);
});

test("a file without tokens is an error, not an empty picker", () => {
  expect(() => parseDevUsers("grants: []")).toThrow(/no tokens/i);
  expect(() => parseDevUsers("<html>404</html>")).toThrow(/no tokens/i);
});
