// A modal dialog on the native <dialog>: the page behind is inert, focus
// stays inside, Escape closes it.
import { useEffect, useId, useRef, type ReactElement, type ReactNode } from "react";

export function Dialog({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }): ReactElement {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    // jsdom has no showModal; a browser always does.
    if (typeof d.showModal === "function") {
      if (!d.open) d.showModal();
    } else d.setAttribute("open", "");
    return () => {
      if (typeof d.close === "function" && d.open) d.close();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby={titleId}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
    >
      <h2 id={titleId} className="dialog-title">
        {title}
      </h2>
      {children}
    </dialog>
  );
}
