// A save that only changes sets merges with other people's changes, so the
// Item page sends it without an etag (Jira part 2, step 2).
import { describe, expect, test } from "vitest";
import { story3 } from "../test/fixtures";
import { onlySetOps } from "./values";

describe("onlySetOps", () => {
  test("a patch of set operations only", () => {
    expect(onlySetOps(story3, { f_labels: { add: ["api"] }, f_reviewers: { remove: ["user:sam"] } })).toBe(true);
  });
  test("a plain field beside a set: the etag is needed", () => {
    expect(onlySetOps(story3, { f_summary: "New", f_labels: { add: ["api"] } })).toBe(false);
  });
  test("an empty patch is nothing to send", () => {
    expect(onlySetOps(story3, {})).toBe(false);
  });
});
