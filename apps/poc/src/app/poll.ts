// There are no live updates (the API has no Watch): a visible page reloads
// every so often, and never while the tab is hidden.
import { useEffect, useRef, useState } from "react";

// Calls reload every intervalMs while the document is visible and
// `paused` is false; on becoming visible again after missing a turn, at
// once.
export function usePoll(reload: () => void, intervalMs: number, paused = false): void {
  // The latest reload, so a new function each render restarts nothing.
  const latest = useRef(reload);
  latest.current = reload;
  useEffect(() => {
    if (paused) return;
    let last = Date.now();
    const tick = () => {
      last = Date.now();
      latest.current();
    };
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") tick();
    }, intervalMs);
    const onVisible = () => {
      if (document.visibilityState === "visible" && Date.now() - last >= intervalMs) tick();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [intervalMs, paused]);
}

// The time now, every intervalMs: for countdowns, without any read.
export function useNow(intervalMs: number): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), intervalMs);
    return () => clearInterval(timer);
  }, [intervalMs]);
  return now;
}
