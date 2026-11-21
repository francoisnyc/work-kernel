// A conversation: replies (public), notes (internal) and comments, each an
// article named by its kind and author. Notes are drawn apart: amber, with
// a lock, and say who can read them.
import type { ReactElement } from "react";
import type { Message } from "../model/conversation";
import { authorLabel, type Audience } from "../model/people";
import { Avatar } from "./Avatar";
import { when } from "./format";
import { Icon } from "./Icon";

const kindWord = { reply: "Reply", note: "Internal note", comment: "Comment" } as const;

export function MessageView({ m, viewer, audience }: { m: Message; viewer: string; audience: Audience }): ReactElement {
  const author = authorLabel(m.author, viewer, audience);
  const mine = m.author === viewer;
  return (
    <article
      className={`message message-${m.kind}${mine && m.kind !== "note" ? " message-mine" : ""}`}
      aria-label={`${kindWord[m.kind]} by ${author}, ${when(m.at)}`}
    >
      <header className="message-head">
        <Avatar principal={m.author} />
        <span className="message-author">{author}</span>
        {m.kind === "note" && (
          <span className="message-kind">
            <Icon name="lock" />
            Internal note · staff only
          </span>
        )}
        <time className="message-time" dateTime={m.at}>
          {when(m.at)}
        </time>
      </header>
      <p className="message-body">{m.body}</p>
    </article>
  );
}
