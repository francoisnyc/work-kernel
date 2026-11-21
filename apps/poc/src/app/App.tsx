// The app: sign-in (dev only), then the areas a person may open.
import { useEffect, useMemo, useState, type ReactElement } from "react";
import { areasFor, homeFor, parseDevUsers, type Area, type DevUser } from "../model/devusers";
import { DeskRoutes } from "../pages/desk/DeskRoutes";
import { PortalRoutes } from "../pages/portal/PortalRoutes";
import { SignIn } from "../pages/SignIn";
import { TrackerRoutes } from "../pages/tracker/TrackerRoutes";
import { Empty } from "../ui/Page";
import { RefusalNotice } from "../ui/RefusalNotice";
import { Announcer } from "./announce";
import { apiFor } from "./api";
import { go, useRoute } from "./router";
import { Session, SessionProvider } from "./session";
import { Shell } from "./Shell";

// DEV ONLY. The sign-in list exists only in a build made with
// VITE_DEV_USERS=1 (the compose stack's): Vite replaces the flag at build
// time, so without it this branch is dead code, and the dev-user parser and
// the YAML library are shaken out of the bundle (the CI build checks it).
// There is no other sign-on yet.
const DEV_USERS = import.meta.env.VITE_DEV_USERS === "1";
const CHOSEN = "pli.poc.user"; // sessionStorage: per tab, so two tabs can be two people

async function loadDevUsers(): Promise<DevUser[]> {
  const res = await fetch("/dev-tokens.yaml", { cache: "no-store" });
  if (!res.ok) throw new Error(`the dev-token file is not served here (HTTP ${res.status})`);
  return parseDevUsers(await res.text());
}

function chosenName(): string {
  try {
    return sessionStorage.getItem(CHOSEN) ?? "";
  } catch {
    return "";
  }
}

export function App(): ReactElement {
  const [users, setUsers] = useState<DevUser[]>();
  const [chosen, setChosen] = useState(chosenName);
  const [error, setError] = useState<unknown>();
  const route = useRoute();

  useEffect(() => {
    if (DEV_USERS) loadDevUsers().then(setUsers, setError);
  }, []);

  const user = users?.find((u) => u.name === chosen);
  const session = useMemo(
    () => (user && users ? new Session(user, apiFor(user.token), users.filter((u) => u.namespace === user.namespace)) : undefined),
    [user, users],
  );

  // Somewhere to go: a signed-in person at the root goes home.
  useEffect(() => {
    if (user && route.length === 0) go(homeFor(user));
  }, [user, route.length]);

  if (!DEV_USERS) {
    return (
      <main id="main" className="signin">
        <Empty title="No sign-on in this build">
          <p>This build has no sign-on: there is none yet. The compose stack's build signs in with dev tokens.</p>
        </Empty>
      </main>
    );
  }
  if (error) {
    return (
      <main id="main" className="signin">
        <RefusalNotice error={error} />
      </main>
    );
  }
  if (!users) return <main id="main" className="signin" aria-busy="true" />;

  if (!session || route[0] === "signin") {
    return (
      <SignIn
        users={users}
        onSignIn={(name) => {
          try {
            sessionStorage.setItem(CHOSEN, name);
          } catch {
            // Without storage the choice lasts for this page.
          }
          setChosen(name);
          go(homeFor(users.find((u) => u.name === name)!));
        }}
      />
    );
  }

  const area = (route[0] ?? "") as Area;
  const allowed = areasFor(session.user).includes(area);
  return (
    <SessionProvider value={session}>
      <Announcer>
        <Shell area={allowed ? area : areasFor(session.user)[0]!}>
          {!allowed ? (
            <main id="main" tabIndex={-1} className="main">
              <Empty title="Nothing here for you">
                <a href={homeFor(session.user)}>Go to your start page</a>
              </Empty>
            </main>
          ) : area === "desk" ? (
            <DeskRoutes route={route.slice(1)} />
          ) : area === "portal" ? (
            <PortalRoutes route={route.slice(1)} />
          ) : (
            <TrackerRoutes route={route.slice(1)} />
          )}
        </Shell>
      </Announcer>
    </SessionProvider>
  );
}
