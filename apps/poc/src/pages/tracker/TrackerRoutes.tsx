// The tracker (PLAT-like spaces): spaces, a space's backlog, its active
// sprint's board, an issue, its epics and an epic.
import type { Item } from "@gen/kernel/v1/kernel_pb";
import type { ReactElement, ReactNode } from "react";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { Icon } from "../../ui/Icon";
import { BacklogPage } from "./BacklogPage";
import { BoardPage } from "./BoardPage";
import { EpicPage, EpicsPage } from "./EpicsPage";
import { IssuePage } from "./IssuePage";
import { SpacesPage } from "./SpacesPage";

export function TrackerRoutes({ route }: { route: string[] }): ReactElement {
  const [spaceId, page, id] = route;
  if (!spaceId) return <SpacesPage />;
  if (page === "board") return <BoardPage spaceId={spaceId} />;
  if (page === "epics") return <EpicsPage spaceId={spaceId} />;
  if (page === "issue" && id) return <IssuePage spaceId={spaceId} id={id} />;
  if (page === "epic" && id) return <EpicPage spaceId={spaceId} id={id} />;
  return <BacklogPage spaceId={spaceId} />;
}

// The space, read once to name it (its key makes the issue keys).
export function useSpace(spaceId: string): Item | undefined {
  const s = useSession();
  return useLoad(() => s.item(`${s.ns}/items/${spaceId}`), [spaceId], { poll: false }).data;
}

export const spaceKey = (space: Item | undefined) => String(space?.fields?.f_key ?? "");

export function TrackerFrame({ spaceId, space, current, children }: { spaceId?: string; space?: Item; current?: "backlog" | "board" | "epics"; children: ReactNode }): ReactElement {
  const link = (page: "backlog" | "board" | "epics", label: string, icon: "list" | "board" | "flag") => (
    <a href={`#/tracker/${spaceId}/${page}`} aria-current={current === page ? "page" : undefined}>
      <Icon name={icon} />
      {label}
    </a>
  );
  return (
    <div className="layout">
      <nav className="sidenav" aria-label="Tracker">
        <p className="sidenav-title">Tracker</p>
        <a href="#/tracker">
          <Icon name="inbox" />
          All spaces
        </a>
        {spaceId && (
          <>
            <p className="sidenav-title" style={{ marginTop: 16 }}>
              {String(space?.fields?.f_name ?? "Space")}
            </p>
            {link("backlog", "Backlog", "list")}
            {link("board", "Board", "board")}
            {link("epics", "Epics", "flag")}
          </>
        )}
      </nav>
      <main id="main" tabIndex={-1} className="main">
        {children}
      </main>
    </div>
  );
}
