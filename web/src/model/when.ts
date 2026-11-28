// Dates and times in words (the Inbox, the look, item 3).
// STUB for the Phase B tests: the words come in Phase C.

export type WhenOpts = { locale?: string; timeZone?: string };

// "today 1:43 pm", "tomorrow 9:00 am", "28 Nov"; "" for anything that is
// not an instant.
export function dayAndTime(_instant: string, _now: Date, _opts: WhenOpts = {}): string {
  return "";
}

// "4 h ago", "in 19 h", "now"; "" for anything that is not an instant.
export function fromNow(_instant: string, _now: Date): string {
  return "";
}

// The whole instant, for a tooltip; "" for anything that is not an instant.
export function fullInstant(_instant: string, _opts: WhenOpts = {}): string {
  return "";
}
