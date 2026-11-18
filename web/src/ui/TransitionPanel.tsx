// The moves an Item's Type declares from its current state, as buttons.
// Nothing is evaluated here: every transition is offered, its rules are
// shown as text, and the server's answer (an outcome, or a refusal) is
// shown as it came.
//
// Phase B: signature only.
import type { JsonObject } from "@bufbuild/protobuf";
import type { ReactElement } from "react";
import type { Item, Transition, TransitionItemResponse, Type } from "../gen/kernel/v1/kernel_pb";

export type TransitionPanelProps = {
  type: Type;
  item: Item;
  principals?: string[];
  // Only these transitions (the inbox's rows); default: all leaving the
  // Item's state.
  only?: Transition[];
  // fields: the patch of the transition's required fields, changed ones
  // only. requestId: new for each move asked; the same again when the user
  // asks again about a PENDING one.
  onTransition: (toState: string, fields: JsonObject, requestId: string) => Promise<TransitionItemResponse>;
};

export function TransitionPanel(_props: TransitionPanelProps): ReactElement {
  throw new Error("not implemented");
}
