import { useEffect, useState, type ReactElement } from "react";
import { customerView, isCustomer, pickerLabel } from "../model/customers";
import type { DevUser } from "../model/devusers";
import { BoardPage } from "../pages/BoardPage";
import { InboxPage } from "../pages/InboxPage";
import { ItemPage } from "../pages/ItemPage";
import { ItemsPage } from "../pages/ItemsPage";
import { NewItemPage } from "../pages/NewItemPage";
import { TypesPage } from "../pages/TypesPage";
import { RefusalView } from "../ui/RefusalView";
import { parseHash, useHash } from "./router";
import { Session, SessionProvider, apiFor } from "./session";

// DEV ONLY. The picker exists only in a build made with VITE_DEV_USERS=1
// (the compose stack's): Vite replaces the flag at build time, so without
// it this branch, the dev-user parser and the YAML library are not in the
// bundle at all. There is no other sign-on yet.
const DEV_USERS = import.meta.env.VITE_DEV_USERS === "1";
const CHOSEN = "pli.actAs"; // sessionStorage: per tab, so two tabs can be two users

async function loadDevUsers(): Promise<DevUser[]> {
  const { parseDevUsers } = await import("../model/devusers");
  const res = await fetch("/dev-tokens.yaml", { cache: "no-store" });
  if (!res.ok)
    throw new Error(
      `the dev-token file is not served here (HTTP ${res.status})`,
    );
  return parseDevUsers(await res.text());
}

export function App(): ReactElement {
  const [users, setUsers] = useState<DevUser[]>([]);
  const [chosen, setChosen] = useState(
    () => sessionStorage.getItem(CHOSEN) ?? "",
  );
  const [session, setSession] = useState<Session>();
  const [error, setError] = useState<unknown>();
  const route = parseHash(useHash());

  useEffect(() => {
    if (DEV_USERS) loadDevUsers().then(setUsers, setError);
  }, []);

  // Choosing a user starts over: their client, their namespace's Types.
  const user = users.find((u) => u.name === chosen);
  useEffect(() => {
    setSession(undefined);
    if (!user) return;
    let current = true;
    const api = apiFor(user.token);
    api.listTypes({ parent: `namespaces/${user.namespace}` }).then(
      (res) => {
        if (!current) return;
        const principals = users
          .filter((u) => u.namespace === user.namespace)
          .map((u) => u.principal);
        // A customer meets every Type as it is shown to them: its customer
        // fields, and only the moves whose allowed names creator.
        const types = isCustomer(user)
          ? res.types.map(customerView)
          : res.types;
        setSession(new Session(user, api, types, principals));
        setError(undefined);
      },
      (e) => current && setError(e),
    );
    return () => {
      current = false;
    };
  }, [user, users]);

  function actAs(name: string) {
    sessionStorage.setItem(CHOSEN, name);
    setChosen(name);
  }

  return (
    <>
      <header className="banner">
        {DEV_USERS ? (
          <>
            <strong>DEV</strong>{" "}
            <span>
              {user
                ? `acting as ${user.name} (${user.principal}, ${user.namespace})`
                : "nobody chosen"}
            </span>
            <span className="warning"> dev tokens, not real sign-on</span>
            <label htmlFor="act-as">Act as</label>
            <select
              id="act-as"
              value={chosen}
              onChange={(e) => actAs(e.target.value)}
            >
              <option value="">(choose a dev user)</option>
              {users.map((u) => (
                <option key={u.name} value={u.name}>
                  {pickerLabel(u)}
                </option>
              ))}
            </select>
          </>
        ) : (
          <strong>Pli</strong>
        )}
        <nav aria-label="Main">
          <a href="#/">Items</a>
          <a href="#/search">Search</a>
          <a href="#/inbox">Inbox</a>
          <a href="#/new">New Item</a>
          <a href="#/types">Types</a>
        </nav>
      </header>
      <main>
        {error !== undefined && <RefusalView error={error} />}
        {!DEV_USERS && (
          <p>
            This build has no sign-on configured: there is nobody to act as.
          </p>
        )}
        {DEV_USERS && !user && (
          <p>Choose a dev user in the banner to act as them.</p>
        )}
        {session && (
          // A new key per user: every page starts over when the user changes.
          <SessionProvider value={session} key={session.user.name}>
            <Page parts={route.parts} query={route.query} />
          </SessionProvider>
        )}
      </main>
    </>
  );
}

function Page({
  parts,
  query,
}: {
  parts: string[];
  query: URLSearchParams;
}): ReactElement {
  const [page, a, b] = parts;
  switch (page) {
    case undefined:
      return <ItemsPage mode="browse" query={query} />;
    case "search":
      return <ItemsPage mode="search" query={query} />;
    case "items":
      // #/items alone is the top level, as #/ is.
      return a ? (
        <ItemPage key={a} id={a} />
      ) : (
        <ItemsPage mode="browse" query={query} />
      );
    case "new":
      return <NewItemPage container={query.get("in") ?? ""} />;
    case "inbox":
      return <InboxPage />;
    case "board":
      return (
        <BoardPage
          key={`${a}/${b}`}
          id={a ?? ""}
          link={b ?? ""}
          rankField={query.get("rank") ?? "f_rank"}
        />
      );
    case "types":
      return <TypesPage />;
    default:
      return <p>No such page.</p>;
  }
}
