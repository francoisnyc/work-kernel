// Requests: a process app on the vacation request and the leave balance.
// Every staff member asks for time off and may be someone's approver (the
// Types name no role for either); Balances is for hr_admin.
import { createContext, useContext, useEffect, useState, type ReactElement, type ReactNode } from "react";
import { useSession } from "../../app/session";
import { seesBalances } from "../../model/devusers";
import { Icon } from "../../ui/Icon";
import { BalancesPage } from "./BalancesPage";
import { CasePage } from "./CasePage";
import { DecisionPage } from "./DecisionPage";
import { InboxPage } from "./InboxPage";
import { LeaveFormPage } from "./LeaveFormPage";
import { MyLeave } from "./MyLeave";

// A sentence that outlives a page change ("You approved ana's request."),
// shown by the page it was meant for, until the next page change.
type Flash = { text: string; set: (text: string, page: string) => void };
const FlashContext = createContext<Flash>({ text: "", set: () => {} });
export const useFlash = () => useContext(FlashContext);

export function RequestsRoutes({ route }: { route: string[] }): ReactElement {
  const [flash, setFlash] = useState({ text: "", page: "" });
  const [page, id] = route;
  const here = route.join("/");
  useEffect(() => {
    if (flash.text !== "" && flash.page !== here) setFlash({ text: "", page: "" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [here]);
  const text = flash.page === here ? flash.text : "";
  const set = (t: string, p: string) => setFlash({ text: t, page: p });
  let body: ReactElement;
  if (page === "new") body = <LeaveFormPage />;
  else if (page === "edit" && id) body = <LeaveFormPage draftId={id} />;
  else if (page === "inbox") body = <InboxPage />;
  else if (page === "task" && id) body = <DecisionPage id={id} />;
  else if (page === "case" && id) body = <CasePage id={id} />;
  else if (page === "balances") body = <BalancesPage />;
  else body = <MyLeave />;
  return <FlashContext.Provider value={{ text, set }}>{body}</FlashContext.Provider>;
}

export type RequestsPage = "mine" | "inbox" | "balances" | "none";

// The area's frame: its navigation and the page.
export function RequestsFrame({ current, children }: { current: RequestsPage; children: ReactNode }): ReactElement {
  const s = useSession();
  const link = (page: RequestsPage, href: string, icon: Parameters<typeof Icon>[0]["name"], label: string) => (
    <a href={href} aria-current={current === page ? "page" : undefined}>
      <Icon name={icon} />
      {label}
    </a>
  );
  return (
    <div className="layout">
      <nav className="sidenav" aria-label="Requests">
        <p className="sidenav-title">Requests</p>
        {link("mine", "#/requests", "user", "My leave")}
        {link("inbox", "#/requests/inbox", "inbox", "Waiting for you")}
        {seesBalances(s.user) && link("balances", "#/requests/balances", "list", "Balances")}
      </nav>
      <main id="main" tabIndex={-1} className="main">
        {children}
      </main>
    </div>
  );
}

// The flash, once: a polite status region.
export function FlashStatus(): ReactElement | null {
  const flash = useFlash();
  if (flash.text === "") return null;
  return (
    <p role="status" className="notice notice-success">
      <Icon name="check" />
      {flash.text}
    </p>
  );
}
