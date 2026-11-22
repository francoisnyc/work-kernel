// What the Requests pages share: reading a whole history, dates in words,
// a balance in numbers, and a case's timeline.
import type { Item, ItemEvent } from "@gen/kernel/v1/kernel_pb";
import { useId, type ReactElement } from "react";
import type { Session } from "../../app/session";
import type { CaseEntry } from "../../model/casetimeline";
import { optionLabel } from "../../model/labels";
import type { Balance } from "../../model/leave";
import { when } from "../../ui/format";
import { Icon } from "../../ui/Icon";

// An Item's whole history, every page (a request's is short).
export async function readHistory(s: Session, name: string): Promise<ItemEvent[]> {
  const out: ItemEvent[] = [];
  let pageToken = "";
  for (let i = 0; i < 20; i++) {
    const res = await s.api.listItemEvents({ parent: name, pageSize: 500, pageToken });
    out.push(...res.events);
    pageToken = res.nextPageToken;
    if (pageToken === "") break;
  }
  return out;
}

// A calendar date in words, read as a date (no time zone shift).
export function dayLabel(d: unknown): string {
  if (typeof d !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(d)) return "—";
  return new Date(`${d}T00:00:00Z`).toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
}

export function datesLabel(item: Item): string {
  const a = item.fields?.f_start;
  const b = item.fields?.f_end;
  return a === b ? dayLabel(a) : `${dayLabel(a)} – ${dayLabel(b)}`;
}

export const daysOf = (item: Item): string => String(item.fields?.f_days ?? "—");
export const leaveTypeLabel = (item: Item): string => (typeof item.fields?.f_leave_type === "string" ? optionLabel("f_leave_type", item.fields.f_leave_type) : "—");
export const reasonOf = (item: Item): string => (typeof item.fields?.f_comment === "string" ? item.fields.f_comment : "");

// A balance in numbers, as a region named by its title.
export function BalanceSummary({ title, balance, pending, missing }: { title: string; balance?: Balance; pending?: number; missing: string }): ReactElement {
  const id = useId();
  return (
    <section className="card balance" aria-labelledby={id}>
      <h2 id={id} className="balance-title">
        {title}
      </h2>
      {balance ? (
        <>
          <p className="balance-left">
            <span className="balance-number">{balance.left}</span> {balance.left === 1 ? "day" : "days"} left
          </p>
          <p className="muted">
            {balance.used} used of {balance.allowance}
            {pending !== undefined && pending > 0 ? ` · ${pending} waiting for approval` : ""}
          </p>
        </>
      ) : (
        <p className="muted">{missing}</p>
      )}
    </section>
  );
}

// A case's timeline: people's acts, and the system's steps marked as such.
export function CaseTimelineView({ entries }: { entries: CaseEntry[] }): ReactElement {
  const id = useId();
  return (
    <section className="section" aria-labelledby={id}>
      <h2 id={id}>Timeline</h2>
      {entries.length === 0 ? (
        <p className="muted">Nothing has happened yet.</p>
      ) : (
        <ol className="plain case-timeline">
          {entries.map((e, i) => (
            <li key={i} className={`case-entry${e.system ? " case-entry-system" : ""}${e.refused ? " case-entry-refused" : ""}`}>
              <span className="case-entry-mark" aria-hidden="true">
                <Icon name={e.system ? "clock" : e.refused ? "alert" : "user"} size={14} />
              </span>
              <span className="case-entry-text">
                {e.system ? (
                  <>
                    <span className="case-system-tag">System</span> {e.text}
                  </>
                ) : (
                  <>
                    <strong>{e.who}</strong> {e.text}
                  </>
                )}
              </span>
              <time className="history-time" dateTime={e.at}>
                {when(e.at)}
              </time>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
