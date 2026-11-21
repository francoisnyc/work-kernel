// The desk area's screens (built next).
import type { ReactElement } from "react";
import { Empty } from "../../ui/Page";

export function DeskRoutes(_props: { route: string[] }): ReactElement {
  return (
    <main id="main" tabIndex={-1} className="main">
      <Empty title="Coming next" />
    </main>
  );
}
