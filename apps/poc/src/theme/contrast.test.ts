// Every colour pair the stylesheet uses for text or controls meets WCAG AA,
// in both themes: 4.5:1 for text, 3:1 for borders and the focus ring.
import { describe, expect, test } from "vitest";
import { contrast } from "./contrast";
import { tokens } from "./tokens";

test("the contrast formula", () => {
  expect(contrast("#000000", "#ffffff")).toBeCloseTo(21, 5);
  expect(contrast("#ffffff", "#ffffff")).toBeCloseTo(1, 5);
  expect(contrast("#777777", "#ffffff")).toBeCloseTo(4.48, 2);
  expect(contrast("#fff", "#000")).toBeCloseTo(21, 5);
});

// [foreground, background]
const text: [string, string][] = [
  ["text", "bg"],
  ["text", "surface"],
  ["text-muted", "bg"],
  ["text-muted", "surface"],
  ["accent", "surface"], // links
  ["on-accent", "accent-strong"], // primary buttons
  ["tone-neutral-text", "tone-neutral-bg"],
  ["tone-active-text", "tone-active-bg"],
  ["tone-waiting-text", "tone-waiting-bg"],
  ["tone-done-text", "tone-done-bg"],
  ["tone-canceled-text", "tone-canceled-bg"],
  ["danger-text", "danger-bg"], // Breached, Overdue
  ["note-text", "note-bg"], // internal notes
  ["text", "reply-bg"], // public replies
];
const ui: [string, string][] = [
  ["border-strong", "surface"], // inputs
  ["focus", "bg"],
  ["focus", "surface"],
];

describe.each(["light", "dark"] as const)("%s theme", (theme) => {
  const t = tokens[theme];
  test.each(text)("text %s on %s is at least 4.5:1", (fg, bg) => {
    expect(t[fg], fg).toMatch(/^#[0-9a-f]{6}$/);
    expect(t[bg], bg).toMatch(/^#[0-9a-f]{6}$/);
    expect(contrast(t[fg]!, t[bg]!)).toBeGreaterThanOrEqual(4.5);
  });
  test.each(ui)("control %s on %s is at least 3:1", (fg, bg) => {
    expect(t[fg], fg).toMatch(/^#[0-9a-f]{6}$/);
    expect(t[bg], bg).toMatch(/^#[0-9a-f]{6}$/);
    expect(contrast(t[fg]!, t[bg]!)).toBeGreaterThanOrEqual(3);
  });
});

test("both themes define the same tokens", () => {
  expect(Object.keys(tokens.dark).sort()).toEqual(Object.keys(tokens.light).sort());
});
