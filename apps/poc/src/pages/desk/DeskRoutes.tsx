// The service desk, agent side: desks, a desk's queue, an incident.
import type { ReactElement, ReactNode } from "react";
import { Icon } from "../../ui/Icon";
import { DeskList } from "./DeskList";
import { IncidentPage } from "./IncidentPage";
import { QueuePage } from "./QueuePage";

export function DeskRoutes({ route }: { route: string[] }): ReactElement {
  const [page, id, view] = route;
  if (page === "q" && id) return <QueuePage deskId={id} view={view ?? "open"} />;
  if (page === "incident" && id) return <IncidentPage id={id} />;
  return <DeskList />;
}

// The desk area's frame: its navigation and the page.
export function DeskFrame({ desk, children }: { desk?: { id: string; name: string }; children: ReactNode }): ReactElement {
  return (
    <div className="layout">
      <nav className="sidenav" aria-label="Service desk">
        <p className="sidenav-title">Service desk</p>
        <a href="#/desk">
          <Icon name="inbox" />
          All desks
        </a>
        {desk && (
          <>
            <p className="sidenav-title" style={{ marginTop: 16 }}>
              {desk.name}
            </p>
            <a href={`#/desk/q/${desk.id}`}>
              <Icon name="list" />
              Queue
            </a>
          </>
        )}
      </nav>
      <main id="main" tabIndex={-1} className="main">
        {children}
      </main>
    </div>
  );
}
