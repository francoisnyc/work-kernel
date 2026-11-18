// Browse (the top level, through ListItems) and search (SearchItems): one
// page, the same filters. The form lives in the URL, so back, reload and
// links work.
import { useEffect, useState, type ReactElement } from "react";
import { go } from "../app/router";
import { useSession } from "../app/session";
import type { Item } from "../gen/kernel/v1/kernel_pb";
import { emptySearch, toListRequest, toSearchRequest, type SearchForm } from "../model/search";
import { ItemList } from "../ui/ItemList";
import { RefusalView } from "../ui/RefusalView";
import { SearchFormView } from "../ui/SearchFormView";

const PAGE = 50;

export function formFromQuery(query: URLSearchParams): SearchForm {
  try {
    return { ...emptySearch, ...(JSON.parse(query.get("s") ?? "{}") as Partial<SearchForm>) };
  } catch {
    return emptySearch;
  }
}

export const searchHash = (form: SearchForm) => `#/search?s=${encodeURIComponent(JSON.stringify(form))}`;

export function ItemsPage({ mode, query }: { mode: "browse" | "search"; query: URLSearchParams }): ReactElement {
  const session = useSession();
  const s = query.get("s") ?? "";
  const form = formFromQuery(query);
  const [items, setItems] = useState<Item[]>([]);
  const [next, setNext] = useState("");
  const [error, setError] = useState<unknown>();
  const [tick, setTick] = useState(0);
  // A search with neither words nor a filter is not sent: the server
  // would refuse it, and the page has just opened.
  const blank = mode === "search" && s === "";

  async function load(pageToken: string) {
    try {
      const res =
        mode === "search"
          ? await session.api.searchItems(toSearchRequest(session.ns, form, { minSeq: session.minSeq, pageToken, pageSize: PAGE }))
          : await session.api.listItems(toListRequest(session.ns, session.parent, form, { pageToken, pageSize: PAGE }));
      setItems((old) => (pageToken === "" ? res.items : [...old, ...res.items]));
      setNext(res.nextPageToken);
      setError(undefined);
    } catch (e) {
      setError(e);
      if (pageToken === "") setItems([]);
    }
  }

  useEffect(() => {
    if (blank) setItems([]);
    else void load("");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, s, tick]);

  return (
    <>
      <h1>{mode === "search" ? "Search" : "Items at the top level"}</h1>
      <SearchFormView
        key={`${mode}?${s}`}
        initial={form}
        types={session.types}
        words={mode === "search"}
        submitLabel={mode === "search" ? "Search" : "Apply"}
        onSubmit={(f) => go(`#/${mode === "search" ? "search" : ""}?s=${encodeURIComponent(JSON.stringify(f))}`)}
      />
      <p>
        <button type="button" className="quiet" onClick={() => setTick((t) => t + 1)}>
          Refresh
        </button>
      </p>
      {error !== undefined && <RefusalView error={error} />}
      {blank ? <p className="hint">Words, a filter or both.</p> : <ItemList items={items} label="Results" />}
      {next !== "" && (
        <button type="button" onClick={() => load(next)}>
          Load more
        </button>
      )}
    </>
  );
}
