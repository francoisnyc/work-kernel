// Who is signed in, and what every page needs from that: their API client,
// their namespace, the Types (read once), and a cache of Items read only to
// name them (a desk, a link's target).
import type { Item, Type } from "@gen/kernel/v1/kernel_pb";
import { createContext, useContext } from "react";
import { isCustomer, type DevUser } from "../model/devusers";
import type { Audience } from "../model/people";
import type { Api } from "./api";

export class Session {
  // Staff only: the highest commit_seq a write of this tab returned, sent
  // as min_seq so a search sees what the person just did. A customer's
  // writes carry no commit_seq and a customer never sends min_seq.
  private seq = 0n;
  private types = new Map<string, Promise<Type>>();
  private items = new Map<string, Promise<Item | undefined>>();

  constructor(
    readonly user: DevUser,
    readonly api: Api,
    // Everyone the dev-token file names in this namespace (no directory).
    readonly people: DevUser[],
  ) {}

  get ns(): string {
    return `namespaces/${this.user.namespace}`;
  }
  get customer(): boolean {
    return isCustomer(this.user);
  }
  get audience(): Audience {
    return this.customer ? "customer" : "staff";
  }
  get me(): string {
    return this.user.principal;
  }

  typeName(id: string, version: number): string {
    return `${this.ns}/types/${id}/versions/${version}`;
  }
  typeFilter(...ids: string[]): string[] {
    return ids.map((id) => `${this.ns}/types/${id}`);
  }

  // A Type version, read once per session.
  type(name: string): Promise<Type> {
    let t = this.types.get(name);
    if (!t) {
      t = this.api.getType({ name }).then((r) => r.type!);
      this.types.set(name, t);
      t.catch(() => this.types.delete(name));
    }
    return t;
  }

  // An Item read to name it; undefined when it is hidden or gone.
  // Refreshed with fresh = true.
  item(name: string, fresh = false): Promise<Item | undefined> {
    let it = fresh ? undefined : this.items.get(name);
    if (!it) {
      it = this.api.getItem({ name }).then(
        (r) => r.item,
        () => undefined,
      );
      this.items.set(name, it);
    }
    return it;
  }

  // After a write: remember its commit_seq (staff only).
  wrote(res: { commitSeq: bigint }): void {
    if (!this.customer && res.commitSeq > this.seq) this.seq = res.commitSeq;
  }

  // What a staff search adds for read-your-writes; nothing for a customer.
  get readYourWrites(): { minSeq?: bigint } {
    return this.customer || this.seq === 0n ? {} : { minSeq: this.seq };
  }

  // Staff in this namespace, for pickers; desk agents only when asked.
  staff(role?: string): DevUser[] {
    return this.people.filter((u) => !isCustomer(u) && (!role || u.roles.includes(role)));
  }
}

const SessionContext = createContext<Session | undefined>(undefined);
export const SessionProvider = SessionContext.Provider;

export function useSession(): Session {
  const s = useContext(SessionContext);
  if (!s) throw new Error("no session: a page was rendered before anyone signed in");
  return s;
}
