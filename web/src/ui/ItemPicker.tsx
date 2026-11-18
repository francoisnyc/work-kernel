// Choosing another Item: paste its name (or its ID), or find it by words.
import { useState, type ReactElement } from "react";
import type { Item } from "../gen/kernel/v1/kernel_pb";
import { shortId } from "../model/typeinfo";

export type ItemSearch = (words: string, typeNames: string[]) => Promise<Item[]>;

export type ItemPickerProps = {
  id: string;
  // The chosen Item's name, an ID, or words being typed.
  value: string;
  onChange: (value: string) => void;
  // Without it the picker takes a pasted name only.
  search?: ItemSearch;
  // Versionless Type names the Item may be of; empty for any.
  typeNames?: string[];
  titleOf?: (item: Item) => string;
};

const isName = (text: string) => /^namespaces\/[^/]+\/items\/[^/]+$/.test(text) || /^[0-9a-f-]{36}$/i.test(text);

export function ItemPicker({ id, value, onChange, search, typeNames, titleOf }: ItemPickerProps): ReactElement {
  const [found, setFound] = useState<Item[]>();
  const [failed, setFailed] = useState(false);

  async function find() {
    if (!search) return;
    setFailed(false);
    try {
      setFound(await search(value.trim(), typeNames ?? []));
    } catch {
      setFound(undefined);
      setFailed(true);
    }
  }

  const words = value.trim();
  return (
    <span className="picker">
      <input
        id={id}
        type="text"
        placeholder={search ? "words, or an Item's name" : "an Item's name"}
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          setFound(undefined);
        }}
      />
      {search && words !== "" && !isName(words) && (
        <button type="button" className="quiet" onClick={find}>
          Find
        </button>
      )}
      {failed && <span className="hint"> the search was refused: try other words</span>}
      {found && found.length === 0 && <span className="hint"> nothing found</span>}
      {found && found.length > 0 && (
        <span className="picker-results">
          {found.map((it) => (
            <button
              key={it.name}
              type="button"
              className="quiet"
              onClick={() => {
                onChange(it.name);
                setFound(undefined);
              }}
            >
              {titleOf ? titleOf(it) : shortId(it.name)}
            </button>
          ))}
        </span>
      )}
    </span>
  );
}
