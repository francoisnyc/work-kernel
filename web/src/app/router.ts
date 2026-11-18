// A hash router: #/items/{id}?key=value. The hash never reaches the
// server, so the web container needs no route of its own, and back,
// reload and links work.
import { useSyncExternalStore } from "react";

export type Route = { parts: string[]; query: URLSearchParams };

export function parseHash(hash: string): Route {
  const [path = "", query = ""] = hash.replace(/^#\/?/, "").split("?");
  return { parts: path.split("/").filter((p) => p !== "").map(decodeURIComponent), query: new URLSearchParams(query) };
}

function subscribe(onChange: () => void) {
  window.addEventListener("hashchange", onChange);
  return () => window.removeEventListener("hashchange", onChange);
}

export function useHash(): string {
  return useSyncExternalStore(subscribe, () => window.location.hash);
}

export function go(hash: string) {
  window.location.hash = hash;
}
