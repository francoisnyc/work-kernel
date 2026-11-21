// An epic's progress, from its rollup (counts by category, over the issues
// the reader can see): done out of all but the canceled ones.

export type Progress = { done: number; total: number; canceled: number; percent: number };

export function progress(counts: Record<string, bigint | number> | undefined): Progress {
  const n = (k: string) => Number(counts?.[k] ?? 0);
  const all = Object.keys(counts ?? {}).reduce((sum, k) => sum + n(k), 0);
  const canceled = n("canceled");
  const total = all - canceled;
  const done = n("done");
  return { done, total, canceled, percent: total === 0 ? 0 : Math.floor((done * 100) / total) };
}

// "3 of 5 done"; "No issues yet" when there are none.
export function progressLabel(p: Progress): string {
  return p.total === 0 ? "No issues yet" : `${p.done} of ${p.total} done`;
}
