// An epic's progress, from its rollup (counts by category, over the issues
// the reader can see): done out of all but the canceled ones.
import { todo } from "./todo";

export type Progress = { done: number; total: number; canceled: number; percent: number };

export function progress(_counts: Record<string, bigint | number> | undefined): Progress {
  return todo();
}

// "3 of 5 done"; "No issues yet" when there are none.
export function progressLabel(_p: Progress): string {
  return todo();
}
