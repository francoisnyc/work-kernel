// The pieces every screen uses: its header (title, Refresh and when it was
// read), loading and empty states, a state pill.
import type { StateCategory } from "@gen/kernel/v1/kernel_pb";
import type { ReactElement, ReactNode } from "react";
import { useNow } from "../app/poll";
import type { Load } from "../app/useLoad";
import { updatedLabel } from "../model/countdown";
import { categoryTone } from "../model/labels";
import { Icon } from "./Icon";
import { RefusalNotice } from "./RefusalNotice";

export function PageHeader({
  title,
  eyebrow,
  load,
  actions,
  children,
}: {
  title: ReactNode;
  eyebrow?: ReactNode;
  load?: Pick<Load<unknown>, "reload" | "loadedAt" | "loading">;
  actions?: ReactNode;
  children?: ReactNode;
}): ReactElement {
  const now = useNow(5_000);
  return (
    <header className="page-header">
      <div className="page-heading">
        {eyebrow && <p className="eyebrow">{eyebrow}</p>}
        <h1>{title}</h1>
        {children}
      </div>
      <div className="page-actions">
        {actions}
        {load && (
          <>
            <span className="updated" aria-live="off">
              {updatedLabel(load.loadedAt, now)}
            </span>
            <button type="button" className="button button-quiet" onClick={load.reload} disabled={load.loading && !load.loadedAt}>
              <Icon name="refresh" />
              Refresh
            </button>
          </>
        )}
      </div>
    </header>
  );
}

// The first read: skeleton lines; a failed read: the refusal and a retry.
export function Loading<T>({ load, children }: { load: Load<T>; children: (data: T) => ReactNode }): ReactElement {
  if (load.data !== undefined) {
    return (
      <>
        {load.error !== undefined && <RefusalNotice error={load.error} />}
        {children(load.data)}
      </>
    );
  }
  if (load.error !== undefined) {
    return (
      <div className="stack">
        <RefusalNotice error={load.error} />
        <p>
          <button type="button" className="button" onClick={load.reload}>
            Try again
          </button>
        </p>
      </div>
    );
  }
  return (
    <div className="skeleton" aria-busy="true" aria-label="Loading">
      <span />
      <span />
      <span />
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }): ReactElement {
  return (
    <div className="empty">
      <p className="empty-title">{title}</p>
      {children && <div className="empty-body">{children}</div>}
    </div>
  );
}

export function StatePill({ category, label, id }: { category: StateCategory; label: string; id?: string }): ReactElement {
  return (
    <span id={id} className={`pill tone-${categoryTone(category)}`}>
      {label}
    </span>
  );
}
