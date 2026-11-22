// @vitest-environment node
// Jira 2.4b, PR 2: an approver finds the approvals waiting for them in the
// generic Inbox, with no code that knows the access request. The Inbox is
// built from every transition whose `allowed` names field:X; approval@1's
// decisions and its hand-over name field:f_approver, from pending. The
// transitions are read from the repository's Type file, as the API would
// serve them.
import { create } from "@bufbuild/protobuf";
import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { parse } from "yaml";
import { TransitionSchema, TypeSchema } from "../gen/kernel/v1/kernel_pb";
import { inboxQueries } from "./typeinfo";
import { NS } from "../test/fixtures";

type TransitionDoc = { from: string; to: string; allowed?: string[] };

const doc = parse(readFileSync(new URL("../../../types/approval.v1.yaml", import.meta.url), "utf8")) as {
  id: string;
  version: number;
  transitions: TransitionDoc[];
};

const approval = create(TypeSchema, {
  name: `${NS}/types/${doc.id}/versions/${doc.version}`,
  typeId: doc.id,
  version: doc.version,
  transitions: doc.transitions.map((t) => create(TransitionSchema, { fromState: t.from, toState: t.to, allowed: t.allowed ?? [] })),
});

test("approval@1: the Inbox asks for pending approvals whose f_approver is me", () => {
  expect(inboxQueries([approval])).toEqual([{ type: `${NS}/types/approval`, state: "pending", field: "f_approver" }]);
});
