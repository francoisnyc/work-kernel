// One assertive live region for the whole app: what a drag or a keyboard
// move did, said to screen readers.
import { createContext, useCallback, useContext, useState, type ReactElement, type ReactNode } from "react";

const AnnounceContext = createContext<(text: string) => void>(() => {});

export function Announcer({ children }: { children: ReactNode }): ReactElement {
  const [text, setText] = useState("");
  const announce = useCallback((t: string) => setText(t), []);
  return (
    <AnnounceContext.Provider value={announce}>
      {children}
      <div className="sr-only" aria-live="assertive" aria-atomic="true">
        {text}
      </div>
    </AnnounceContext.Provider>
  );
}

export const useAnnounce = () => useContext(AnnounceContext);
