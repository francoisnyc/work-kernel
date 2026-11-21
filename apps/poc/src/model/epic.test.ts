// An epic's progress: done out of all but the canceled, from the rollup.
import { expect, test } from "vitest";
import { progress, progressLabel } from "./epic";

test("done out of all but the canceled", () => {
  const p = progress({ todo: 2n, active: 1n, done: 1n, canceled: 1n });
  expect(p).toEqual({ done: 1, total: 4, canceled: 1, percent: 25 });
  expect(progressLabel(p)).toBe("1 of 4 done");
});

test("no issues, or none the reader can see", () => {
  expect(progress(undefined)).toEqual({ done: 0, total: 0, canceled: 0, percent: 0 });
  expect(progress({})).toEqual({ done: 0, total: 0, canceled: 0, percent: 0 });
  expect(progressLabel(progress({}))).toBe("No issues yet");
});

test("all canceled: nothing left to do", () => {
  expect(progress({ canceled: 2 })).toEqual({ done: 0, total: 0, canceled: 2, percent: 0 });
});

test("the percentage is rounded down", () => {
  expect(progress({ done: 2, todo: 1 }).percent).toBe(66);
});
