// The frame around every signed-in screen: the apps a person may open, the
// person menu (who, roles, theme, switch user) and the skip link.
import { useEffect, useRef, useState, type ReactElement, type ReactNode } from "react";
import { areasFor, type Area } from "../model/devusers";
import { applyTheme, savedTheme, type ThemeChoice } from "../theme/css";
import { Avatar } from "../ui/Avatar";
import { useSession } from "./session";

const areaLabels: Record<Area, string> = { desk: "Service desk", tracker: "Tracker", portal: "Help center" };

export function Shell({ area, children }: { area: Area; children: ReactNode }): ReactElement {
  const s = useSession();
  const areas = areasFor(s.user);
  return (
    <div className={`app app-${area}`}>
      <a className="skip-link" href="#main" onClick={(e) => {
        e.preventDefault();
        document.getElementById("main")?.focus();
      }}>
        Skip to content
      </a>
      <header className="topbar">
        <a className="brand" href={`#/${areas[0]}`}>
          Pli
        </a>
        {s.customer ? (
          <span className="topbar-title">Help center</span>
        ) : (
          <nav aria-label="Apps" className="apps">
            {areas.map((a) => (
              <a key={a} href={`#/${a}`} className="apps-link" aria-current={a === area ? "page" : undefined}>
                {areaLabels[a]}
              </a>
            ))}
          </nav>
        )}
        <span className="dev-badge" title="Dev tokens, not real sign-on">
          DEV
        </span>
        <PersonMenu />
      </header>
      {children}
    </div>
  );
}

function PersonMenu(): ReactElement {
  const s = useSession();
  const [open, setOpen] = useState(false);
  const [theme, setTheme] = useState<ThemeChoice>(savedTheme);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === "Escape" : !box.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", close);
    };
  }, [open]);
  return (
    <div className="person" ref={box}>
      <button
        type="button"
        className="person-button"
        aria-label={`Signed in as ${s.user.name}`}
        aria-expanded={open}
        aria-controls="person-panel"
        onClick={() => setOpen(!open)}
      >
        <Avatar principal={s.me} size={28} />
        <span aria-hidden="true">{s.user.name}</span>
      </button>
      {open && (
        <div className="person-panel" id="person-panel">
          <p className="person-who">
            <strong>{s.user.name}</strong>
            <span className="muted">{s.me}</span>
            <span className="muted">{s.user.roles.length > 0 ? `Roles: ${s.user.roles.join(", ")}` : s.customer ? "Customer" : "No role"}</span>
          </p>
          <div className="field">
            <label htmlFor="theme-choice">Theme</label>
            <select
              id="theme-choice"
              value={theme}
              onChange={(e) => {
                const t = e.target.value as ThemeChoice;
                setTheme(t);
                applyTheme(t);
              }}
            >
              <option value="system">Same as the system</option>
              <option value="light">Light</option>
              <option value="dark">Dark</option>
            </select>
          </div>
          <a className="button" href="#/signin" onClick={() => setOpen(false)}>
            Switch user
          </a>
        </div>
      )}
    </div>
  );
}
