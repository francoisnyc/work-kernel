// The customer portal: pick a desk, open a request, your requests, one
// request's public conversation. It is built on what the API returns to a
// customer and nothing else: no etag, no commit_seq (never read), no
// min_seq (never sent), filters on Types and categories only.
import type { ReactElement, ReactNode } from "react";
import { Icon } from "../../ui/Icon";
import { MyRequests } from "./MyRequests";
import { NewRequest } from "./NewRequest";
import { PortalHome } from "./PortalHome";
import { RequestPage } from "./RequestPage";

export function PortalRoutes({ route }: { route: string[] }): ReactElement {
  const [page, id, extra] = route;
  if (page === "new" && id) return <NewRequest deskId={id} />;
  if (page === "requests" && id) return <RequestPage id={id} detailsUnsaved={extra === "unsaved"} />;
  if (page === "requests") return <MyRequests />;
  return <PortalHome />;
}

export function PortalFrame({ current, children }: { current: "home" | "requests" | "none"; children: ReactNode }): ReactElement {
  return (
    <main id="main" tabIndex={-1} className="portal-main">
      <nav aria-label="Help center" className="portal-nav">
        <a href="#/portal" aria-current={current === "home" ? "page" : undefined}>
          <Icon name="chat" /> Get help
        </a>
        <a href="#/portal/requests" aria-current={current === "requests" ? "page" : undefined}>
          <Icon name="inbox" /> My requests
        </a>
      </nav>
      {children}
    </main>
  );
}
