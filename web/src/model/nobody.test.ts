// @vitest-environment node
// Jira 2.4b: allowed: [nobody] is a move no person makes (only a reaction,
// by consent). The UI greys it out as it does a move only the timer makes.
import { create } from "@bufbuild/protobuf";
import { expect, test } from "vitest";
import { TransitionSchema } from "../gen/kernel/v1/kernel_pb";
import { isSystemOnly } from "./typeinfo";

const tr = (allowed: string[]) => create(TransitionSchema, { fromState: "running", toState: "completed", allowed });

test("a move nobody makes is greyed out", () => {
  expect(isSystemOnly(tr(["nobody"]))).toBe(true);
});

test("as before: the timer's moves greyed, others not", () => {
  expect(isSystemOnly(tr(["principal:system:timer"]))).toBe(true);
  expect(isSystemOnly(tr([]))).toBe(false);
  expect(isSystemOnly(tr(["role:sla_admin"]))).toBe(false);
});
