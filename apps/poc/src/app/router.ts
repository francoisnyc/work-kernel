// A hash router: the route is the part after "#/", split on "/".
import { useEffect, useState } from "react";

export function useRoute(): string[] {
  const [hash, setHash] = useState(() => window.location.hash);
  useEffect(() => {
    const on = () => setHash(window.location.hash);
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return hash.replace(/^#\/?/, "").split("/").filter((s) => s !== "").map(decodeURIComponent);
}

export function go(path: string): void {
  window.location.hash = path;
}

// The ID at the end of an Item name, for a route.
export function idOf(name: string): string {
  return name.slice(name.lastIndexOf("/") + 1);
}
