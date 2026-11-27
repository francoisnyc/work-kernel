// A move's label (the Inbox for people, PR 1b): the transition button
// reads the move's label when the Type gives one (Transition.label), and
// "Move to {state}" otherwise. Nothing else on the panel changes; summary,
// help, primary and due are PR 2's.
import { create } from "@bufbuild/protobuf";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import {
  FieldType,
  StateCategory,
  TransitionItemResponseSchema,
  TransitionOutcome,
  TypeSchema,
} from "../gen/kernel/v1/kernel_pb";
import { item, itemName, typeName } from "../test/fixtures";
import { TransitionPanel } from "./TransitionPanel";

// An approval shaped like approval@2: two moves out of pending carry a
// label, the hand-over carries one too, the move only a reaction makes
// carries none.
const approval = create(TypeSchema, {
  name: typeName("approval", 2),
  typeId: "approval",
  version: 2,
  initialState: "pending",
  fields: [
    { id: "f_approver", type: FieldType.PRINCIPAL, label: "Approver" },
    { id: "f_delegate", type: FieldType.PRINCIPAL, label: "Hand over to" },
  ],
  states: [
    {
      id: "pending",
      category: StateCategory.ACTIVE,
      label: "Waiting for a decision",
    },
    { id: "approved", category: StateCategory.DONE, label: "Approved" },
    { id: "rejected", category: StateCategory.DONE, label: "Rejected" },
    { id: "canceled", category: StateCategory.CANCELED },
  ],
  transitions: [
    {
      fromState: "pending",
      toState: "approved",
      allowed: ["field:f_approver"],
      label: "Approve",
      primary: true,
    },
    {
      fromState: "pending",
      toState: "rejected",
      allowed: ["field:f_approver"],
      label: "Reject",
      help: "It ends here",
    },
    {
      fromState: "pending",
      toState: "pending",
      allowed: ["field:f_approver"],
      required: ["f_delegate"],
      label: "Hand over",
    },
    { fromState: "pending", toState: "canceled", allowed: ["nobody"] },
  ],
});

const pending = item({
  name: itemName(7),
  type: approval.name,
  state: "pending",
  category: StateCategory.ACTIVE,
  fields: { f_approver: "user:bob" },
});

const applied = () =>
  vi
    .fn()
    .mockResolvedValue(
      create(TransitionItemResponseSchema, {
        outcome: TransitionOutcome.APPLIED,
      }),
    );

describe("a move's label", () => {
  test("the button reads the move's label, and Move to {state} where it has none", () => {
    render(
      <TransitionPanel
        type={approval}
        item={pending}
        onTransition={applied()}
      />,
    );
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Approve",
      "Reject",
      "Hand over",
      "Move to canceled",
    ]);
  });

  test("a labelled button still makes its move, to its state", async () => {
    const onTransition = applied();
    render(
      <TransitionPanel
        type={approval}
        item={pending}
        onTransition={onTransition}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(onTransition).toHaveBeenCalledTimes(1);
    expect(onTransition.mock.calls[0]![0]).toBe("approved");
  });

  test("a labelled move with required fields asks for them, then makes its move", async () => {
    const onTransition = applied();
    render(
      <TransitionPanel
        type={approval}
        item={pending}
        onTransition={onTransition}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Hand over" }));
    expect(onTransition).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: /confirm move to pending/i }),
    ).toBeInTheDocument();
  });

  test("`only` keeps the label (an Inbox row)", () => {
    render(
      <TransitionPanel
        type={approval}
        item={pending}
        only={approval.transitions.filter((t) => t.toState === "approved")}
        onTransition={applied()}
      />,
    );
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Approve",
    ]);
  });
});
