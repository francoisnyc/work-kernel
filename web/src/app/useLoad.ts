import { useCallback, useEffect, useState, type DependencyList } from "react";

// Runs a read when the component mounts, when deps change and on reload().
// There are no live updates (the API has no Watch yet): pages reload after
// an action and on demand.
export function useLoad<T>(read: () => Promise<T>, deps: DependencyList) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<unknown>();
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let current = true; // a slower, older read must not overwrite a newer one
    read().then(
      (d) => {
        if (!current) return;
        setData(d);
        setError(undefined);
      },
      (e) => current && setError(e),
    );
    return () => {
      current = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);
  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { data, error, reload };
}
