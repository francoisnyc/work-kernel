// There are no live updates (the API has no Watch): a visible page reloads
// every so often, and never while the tab is hidden.
import { todo } from "../model/todo";

// Calls reload every intervalMs while the document is visible and
// `paused` is false; on becoming visible again after missing a turn, at
// once.
export function usePoll(_reload: () => void, _intervalMs: number, _paused = false): void {
  todo();
}

// The time now, every intervalMs: for countdowns, without any read.
export function useNow(_intervalMs: number): Date {
  return todo();
}
