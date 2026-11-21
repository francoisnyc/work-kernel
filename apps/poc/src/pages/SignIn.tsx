// DEV ONLY. The sign-in screen: the dev-token file's people, staff and
// customers apart. Compiled in only with VITE_DEV_USERS=1.
import type { ReactElement } from "react";
import { groupUsers, type DevUser } from "../model/devusers";
import { Avatar } from "../ui/Avatar";

export function SignIn({ users, onSignIn }: { users: DevUser[]; onSignIn: (name: string) => void }): ReactElement {
  const { staff, customers } = groupUsers(users);
  const group = (id: string, title: string, hint: string, people: DevUser[]) => (
    <section className="signin-group" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      <p className="muted">{hint}</p>
      <ul className="plain signin-list">
        {people.map((u) => (
          <li key={u.name}>
            <button type="button" className="signin-person" aria-label={`Sign in as ${u.name}`} onClick={() => onSignIn(u.name)}>
              <Avatar principal={u.principal} size={32} />
              <span className="signin-name">{u.name}</span>
              <span className="signin-meta">
                {u.roles.length > 0 ? u.roles.join(", ") : u.principal.startsWith("customer:") ? "customer" : "no role"} · {u.namespace}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
  return (
    <main id="main" className="signin">
      <div className="signin-card">
        <p className="brand">Pli</p>
        <h1>Sign in</h1>
        <p className="notice notice-warning">
          <strong>Dev tokens, not real sign-on.</strong> Anyone who can reach this page can act as everyone below. It is served on
          this machine only.
        </p>
        {group("signin-staff", "Staff", "The tracker, and the service desk for desk agents.", staff)}
        {group("signin-customers", "Customers", "The help center: their own requests only.", customers)}
      </div>
    </main>
  );
}
