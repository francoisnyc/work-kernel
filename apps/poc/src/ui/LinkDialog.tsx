// Adds a link: its kind, then the Item, found by words (a combobox over a
// search). Used for an issue's links and an incident's causes.
import type { Item } from "@gen/kernel/v1/kernel_pb";
import { useEffect, useId, useState, type ReactElement } from "react";
import { Dialog } from "./Dialog";
import { RefusalNotice } from "./RefusalNotice";

export function LinkDialog({
  kinds,
  find,
  describe,
  onAdd,
  onClose,
}: {
  kinds: { value: string; label: string }[];
  find: (words: string) => Promise<Item[]>;
  describe: (it: Item) => string;
  onAdd: (kind: string, target: string) => Promise<void>;
  onClose: () => void;
}): ReactElement {
  const id = useId();
  const [kind, setKind] = useState(kinds[0]?.value ?? "");
  const [words, setWords] = useState("");
  const [found, setFound] = useState<Item[]>([]);
  const [picked, setPicked] = useState<Item>();
  const [active, setActive] = useState(0);
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (words.trim() === "" || (picked && describe(picked) === words)) {
      setFound([]);
      return;
    }
    let current = true;
    const t = setTimeout(() => {
      find(words.trim()).then(
        (items) => current && (setFound(items), setActive(0)),
        (e) => current && setError(e),
      );
    }, 200);
    return () => {
      current = false;
      clearTimeout(t);
    };
  }, [words]); // eslint-disable-line react-hooks/exhaustive-deps

  const choose = (it: Item) => {
    setPicked(it);
    setWords(describe(it));
    setFound([]);
  };

  return (
    <Dialog title="Add link" onClose={onClose}>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          if (!picked) return;
          setBusy(true);
          setError(undefined);
          try {
            await onAdd(kind, picked.name);
            onClose();
          } catch (err) {
            setError(err);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="field">
          <label htmlFor={`${id}-kind`}>Link type</label>
          <select id={`${id}-kind`} value={kind} onChange={(e) => setKind(e.target.value)}>
            {kinds.map((k) => (
              <option key={k.value} value={k.value}>
                {k.label}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor={`${id}-find`}>Find an issue</label>
          <input
            id={`${id}-find`}
            role="combobox"
            aria-expanded={found.length > 0}
            aria-controls={`${id}-options`}
            aria-autocomplete="list"
            aria-activedescendant={found.length > 0 ? `${id}-opt-${active}` : undefined}
            autoComplete="off"
            value={words}
            onChange={(e) => {
              setWords(e.target.value);
              setPicked(undefined);
            }}
            onKeyDown={(e) => {
              if (found.length === 0) return;
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setActive((a) => Math.min(a + 1, found.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActive((a) => Math.max(a - 1, 0));
              } else if (e.key === "Enter") {
                e.preventDefault();
                choose(found[active]!);
              }
            }}
          />
          <ul id={`${id}-options`} role="listbox" aria-label="Matching issues" className="options" hidden={found.length === 0}>
            {found.map((it, i) => (
              <li key={it.name} id={`${id}-opt-${i}`} role="option" aria-selected={i === active} onClick={() => choose(it)}>
                {describe(it)}
              </li>
            ))}
          </ul>
        </div>
        <RefusalNotice error={error} />
        <div className="button-row dialog-actions">
          <button type="button" className="button" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="button button-primary" disabled={!picked || busy}>
            Add link
          </button>
        </div>
      </form>
    </Dialog>
  );
}
