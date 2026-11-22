// One request, as its customer sees it: its status, the public
// conversation with the status changes in between, a reply box, and the
// moves a requester may make (cancel a new request, reopen a resolved one).
import type { Item, ItemEvent, Type } from "@gen/kernel/v1/kernel_pb";
import { useId, useState, type ReactElement } from "react";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { portalThread } from "../../model/conversation";
import { optionLabel, stateLabel, urgencyChoices } from "../../model/labels";
import { movesFrom } from "../../model/moves";
import { Fact } from "../../ui/Facts";
import { when } from "../../ui/format";
import { Icon } from "../../ui/Icon";
import { MessageView } from "../../ui/Messages";
import { MoveButtons } from "../../ui/MoveButtons";
import { Loading, PageHeader, StatePill } from "../../ui/Page";
import { RefusalNotice } from "../../ui/RefusalNotice";
import { PortalFrame } from "./PortalRoutes";

type Data = { item: Item; type: Type; desk?: Item; replies: Item[]; events: ItemEvent[] };

export function RequestPage({ id, detailsUnsaved }: { id: string; detailsUnsaved: boolean }): ReactElement {
  const s = useSession();
  const name = `${s.ns}/items/${id}`;
  const [draft, setDraft] = useState("");
  const load = useLoad<Data>(
    async () => {
      const item = (await s.api.getItem({ name })).item!;
      const [type, desk, replies, events] = await Promise.all([
        s.type(item.type),
        s.item(item.container),
        s.api.listItems({ parent: name, filter: { types: s.typeFilter("reply") }, pageSize: 500 }),
        s.api.listItemEvents({ parent: name, pageSize: 500 }),
      ]);
      return { item, type, desk, replies: replies.items, events: events.events };
    },
    [id],
  );
  return (
    <PortalFrame current="requests">
      <Loading load={load}>
        {(d) => {
          const f = d.item.fields ?? {};
          return (
            <>
              <PageHeader title={String(f.f_title ?? "Request")} eyebrow={String(d.desk?.fields?.f_name ?? "Help desk")} load={load} />
              {d.item.state === "waiting_for_customer" && (
                <p className="notice notice-warning" style={{ marginBottom: 16 }}>
                  <strong>We need your reply.</strong> Answer below so we can carry on.
                </p>
              )}
              {detailsUnsaved && (
                <p className="notice notice-warning" style={{ marginBottom: 16 }}>
                  Your request was sent, but its description wasn't saved. Please add it as a reply below.
                </p>
              )}
              <div className="card" style={{ marginBottom: 20 }}>
                <dl className="facts">
                  <Fact term="Status">
                    <StatePill category={d.item.category} label={stateLabel("incident", d.item.state, "customer")} />
                  </Fact>
                  <Fact term="Urgency">{urgencyChoices.find((c) => c.value === f.f_priority)?.label ?? String(f.f_priority ?? "")}</Fact>
                  {typeof f.f_resolution === "string" && <Fact term="Outcome">{optionLabel("f_resolution", f.f_resolution)}</Fact>}
                  <Fact term="Opened">{when(d.item.createTime)}</Fact>
                </dl>
                <div style={{ marginTop: 12 }}>
                  {movesFrom(d.type, d.item.state, "customer").length > 0 && (
                    <MoveButtons
                      moves={movesFrom(d.type, d.item.state, "customer")}
                      options={{}}
                      ctx={{ typeId: "incident" }}
                      onMove={async (to) => {
                        // A customer has no etag to send.
                        await s.api.transitionItem({ name, requestId: crypto.randomUUID(), toState: to });
                        load.reload();
                      }}
                    />
                  )}
                </div>
              </div>
              <section className="section" aria-labelledby="thread-title">
                <h2 id="thread-title">Conversation</h2>
                <div className="thread">
                  {portalThread(d.replies, d.events).map((e, i) =>
                    e.kind === "message" ? (
                      <MessageView key={e.message.name} m={e.message} viewer={s.me} audience="customer" />
                    ) : (
                      <p key={`s${i}`} className="status-line">
                        <Icon name="flag" />
                        Status changed to {stateLabel("incident", e.state, "customer")} · {when(e.at)}
                      </p>
                    ),
                  )}
                </div>
                <ReplyBox draft={draft} setDraft={setDraft} onSend={async (body) => {
                  await s.api.createItem({ parent: s.ns, requestId: crypto.randomUUID(), type: s.typeName("reply", 2), container: name, fields: { f_body: body } });
                  load.reload();
                }} />
              </section>
            </>
          );
        }}
      </Loading>
    </PortalFrame>
  );
}

function ReplyBox({ draft, setDraft, onSend }: { draft: string; setDraft: (v: string) => void; onSend: (body: string) => Promise<void> }): ReactElement {
  const id = useId();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  return (
    <form
      className="card stack"
      onSubmit={async (e) => {
        e.preventDefault();
        if (draft.trim() === "") return;
        setBusy(true);
        setError(undefined);
        try {
          await onSend(draft.trim());
          setDraft("");
        } catch (err) {
          setError(err);
        } finally {
          setBusy(false);
        }
      }}
    >
      <label htmlFor={id} className="label">
        Your reply
      </label>
      <textarea id={id} rows={4} value={draft} onChange={(e) => setDraft(e.target.value)} />
      <RefusalNotice error={error} />
      <div className="button-row">
        <button type="submit" className="button button-primary" disabled={busy || draft.trim() === ""}>
          Send
        </button>
      </div>
    </form>
  );
}
