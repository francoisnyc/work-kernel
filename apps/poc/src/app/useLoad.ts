// A page's read: on mount, when deps change, on reload() (the Refresh
// button), and every 30 s while the tab is visible and no editor is open.
import { useCallback, useEffect, useState, type DependencyList } from "react";
import { usePoll } from "./poll";

export const POLL_MS = 30_000;

export type Load<T> = {
  data?: T;
  error?: unknown;
  loading: boolean;
  loadedAt?: Date;
  reload: () => void;
};

export function useLoad<T>(read: () => Promise<T>, deps: DependencyList, opts: { poll?: boolean; paused?: boolean } = {}): Load<T> {
  const [state, setState] = useState<{ data?: T; error?: unknown; loadedAt?: Date }>({});
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let current = true; // a slower, older read must not overwrite a newer one
    setLoading(true);
    read().then(
      (data) => current && setState({ data, loadedAt: new Date() }),
      (error) => current && setState((s) => ({ ...s, error, loadedAt: new Date() })),
    ).finally(() => current && setLoading(false));
    return () => {
      current = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);
  // A new deps list starts from nothing (another Item, another view).
  useEffect(() => setState({}), deps); // eslint-disable-line react-hooks/exhaustive-deps
  const reload = useCallback(() => setTick((t) => t + 1), []);
  usePoll(reload, POLL_MS, opts.poll === false || opts.paused === true);
  return { ...state, loading, reload };
}
