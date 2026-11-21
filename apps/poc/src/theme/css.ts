// The tokens as CSS custom properties: light by default, dark when the
// system asks for it, and either when the person chooses one
// (data-theme on <html>).
import { tokens, type Tokens } from "./tokens";

const block = (t: Tokens) =>
  Object.entries(t)
    .map(([k, v]) => `--${k}:${v};`)
    .join("");

export function themeCss(): string {
  return [
    `:root{color-scheme:light;${block(tokens.light)}}`,
    `@media (prefers-color-scheme: dark){:root:not([data-theme="light"]){color-scheme:dark;${block(tokens.dark)}}}`,
    `:root[data-theme="dark"]{color-scheme:dark;${block(tokens.dark)}}`,
  ].join("\n");
}

export type ThemeChoice = "system" | "light" | "dark";
const KEY = "pli.poc.theme";

// The person's choice, kept in this browser (a convenience only).
export function savedTheme(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

export function applyTheme(choice: ThemeChoice): void {
  if (choice === "system") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", choice);
  try {
    localStorage.setItem(KEY, choice);
  } catch {
    // Storage may be unavailable; the choice then lasts for this page.
  }
}
