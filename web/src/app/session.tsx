// Who is acting, and what every page needs from that: a client that sends
// their token, their namespace, and the Types.
import { createClient, type Client, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { createContext, useContext } from "react";
import { ItemService, type Item, type Type } from "../gen/kernel/v1/kernel_pb";
import type { DevUser } from "../model/devusers";
import { displayTitle, shortId } from "../model/typeinfo";

export type Api = Client<typeof ItemService>;

// The public API through the web container's /api proxy (same origin),
// in the Connect protocol with JSON bodies. The caller is the bearer
// token, as for every client.
export function apiFor(token: string): Api {
  const auth: Interceptor = (next) => (req) => {
    req.header.set("Authorization", `Bearer ${token}`);
    return next(req);
  };
  return createClient(ItemService, createConnectTransport({ baseUrl: "/api", interceptors: [auth] }));
}

export class Session {
  // The highest commit_seq a write of this tab returned: sent as min_seq
  // on searches, so a search sees what the user just did.
  private seq = 0n;
  private byName = new Map<string, Type>();

  constructor(
    readonly user: DevUser,
    readonly api: Api,
    readonly types: Type[],
    // Principals to suggest in principal fields.
    readonly principals: string[],
  ) {
    for (const t of types) this.byName.set(t.name, t);
  }

  get ns(): string {
    return this.user.namespace;
  }

  get parent(): string {
    return `namespaces/${this.ns}`;
  }

  typeOf(name: string): Type | undefined {
    return this.byName.get(name);
  }

  titleOf = (item: Item): string => {
    const type = this.typeOf(item.type);
    return type ? displayTitle(type, item) : shortId(item.name);
  };

  // An Item's name from what a user pasted: a name, or a bare ID.
  itemName(text: string): string {
    const t = text.trim();
    return t === "" || t.startsWith("namespaces/") ? t : `${this.parent}/items/${t}`;
  }

  wrote(commitSeq: bigint) {
    if (commitSeq > this.seq) this.seq = commitSeq;
  }

  get minSeq(): bigint {
    return this.seq;
  }

  // Finds Items by words, for the pickers.
  search = async (words: string, typeNames: string[]): Promise<Item[]> => {
    const res = await this.api.searchItems({
      parent: this.parent,
      query: words,
      minSeq: this.seq,
      filter: typeNames.length > 0 ? { types: typeNames } : undefined,
      pageSize: 10,
    });
    return res.items;
  };
}

const SessionContext = createContext<Session | undefined>(undefined);
export const SessionProvider = SessionContext.Provider;

export function useSession(): Session {
  const s = useContext(SessionContext);
  if (!s) throw new Error("no session: a page was rendered before a user was chosen");
  return s;
}
