// A new request in a desk: a title, how urgent it is, how many people it
// affects, and what is happening.
import { useState, type ReactElement } from "react";
import { go, idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { optionLabel, urgencyChoices } from "../../model/labels";
import { openRequest, validateRequest, type NewRequest as Form } from "../../model/requests";
import { Loading, PageHeader } from "../../ui/Page";
import { RefusalNotice } from "../../ui/RefusalNotice";
import { PortalFrame } from "./PortalRoutes";

const impacts = ["minor", "moderate", "significant", "extensive"];
const impactWords: Record<string, string> = {
  minor: "Just me",
  moderate: "A few people",
  significant: "A team or a department",
  extensive: "Everyone",
};

export function NewRequest({ deskId }: { deskId: string }): ReactElement {
  const s = useSession();
  const deskName = `${s.ns}/items/${deskId}`;
  const load = useLoad(() => s.item(deskName), [deskId], { poll: false });
  const [form, setForm] = useState<Form>({ title: "", urgency: "P3", impact: "", description: "" });
  const [problems, setProblems] = useState<ReturnType<typeof validateRequest>>({});
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const set = (k: keyof Form) => (e: { target: { value: string } }) => setForm({ ...form, [k]: e.target.value });

  return (
    <PortalFrame current="none">
      <Loading load={load}>
        {(desk) => (
          <>
            <PageHeader title="New request" eyebrow={desk ? String(desk.fields?.f_name ?? "Help desk") : "Help desk"} />
            <form
              className="card"
              noValidate
              onSubmit={async (e) => {
                e.preventDefault();
                const p = validateRequest(form);
                setProblems(p);
                if (Object.keys(p).length > 0) return;
                setBusy(true);
                setError(undefined);
                try {
                  const { request, detailsSaved } = await openRequest(s.api, s.ns, deskName, form);
                  go(`#/portal/requests/${idOf(request.name)}${detailsSaved ? "" : "/unsaved"}`);
                } catch (err) {
                  setError(err);
                } finally {
                  setBusy(false);
                }
              }}
            >
              <div className="field">
                <label htmlFor="req-title">Title</label>
                <input
                  id="req-title"
                  value={form.title}
                  onChange={set("title")}
                  aria-invalid={problems.title ? true : undefined}
                  aria-describedby={problems.title ? "req-title-error" : undefined}
                />
                {problems.title && (
                  <p id="req-title-error" className="field-error">
                    {problems.title}
                  </p>
                )}
              </div>
              <div className="field">
                <label htmlFor="req-urgency">How urgent is it?</label>
                <select id="req-urgency" value={form.urgency} onChange={set("urgency")}>
                  {urgencyChoices.map((c) => (
                    <option key={c.value} value={c.value}>
                      {c.label}
                    </option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label htmlFor="req-impact">How many people are affected?</label>
                <select id="req-impact" value={form.impact} onChange={set("impact")}>
                  <option value="">I'm not sure</option>
                  {impacts.map((i) => (
                    <option key={i} value={i}>
                      {impactWords[i] ?? optionLabel("f_impact", i)}
                    </option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label htmlFor="req-description">Describe the problem</label>
                <textarea
                  id="req-description"
                  rows={6}
                  value={form.description}
                  onChange={set("description")}
                  aria-invalid={problems.description ? true : undefined}
                  aria-describedby={problems.description ? "req-description-error" : "req-description-hint"}
                />
                {problems.description ? (
                  <p id="req-description-error" className="field-error">
                    {problems.description}
                  </p>
                ) : (
                  <p id="req-description-hint" className="field-hint">
                    What happened, and what you were trying to do.
                  </p>
                )}
              </div>
              <RefusalNotice error={error} />
              <div className="button-row">
                <button type="submit" className="button button-primary" disabled={busy}>
                  Send request
                </button>
                <a href="#/portal">Cancel</a>
              </div>
            </form>
          </>
        )}
      </Loading>
    </PortalFrame>
  );
}
