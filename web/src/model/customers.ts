// Jira part 2, step 4a: what the generic UI needs to know about customers,
// read from the principal and the Types, never from a Type's name.
// Phase B stub: the signatures only.
import type { Transition, Type } from "../gen/kernel/v1/kernel_pb";
import type { DevUser } from "./devusers";

const notBuilt = (): never => {
  throw new Error("customers: not built yet");
};

// isCustomer: the dev user is a customer:<id> principal.
export function isCustomer(_u: Pick<DevUser, "principal">): boolean {
  return notBuilt();
}

// pickerLabel: how the act-as picker names a dev user.
export function pickerLabel(_u: DevUser): string {
  return notBuilt();
}

// creatableByCustomer: the Types (latest version of each ID) a customer may
// create inside an Item of the container's Type: its own and shared ones.
// None at the top level.
export function creatableByCustomer(_types: Type[], _container: Type | undefined): Type[] {
  return notBuilt();
}

// customerMayTake: a customer takes a transition only when its allowed
// names creator (the server still decides).
export function customerMayTake(_t: Transition): boolean {
  return notBuilt();
}
