// Transitions come from the Type; the answer comes from the server. The
// panel evaluates no rule.
import { create } from "@bufbuild/protobuf";
import { Code } from "@connectrpc/connect";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { StateCategory, TransitionItemResponseSchema, TransitionOutcome } from "../gen/kernel/v1/kernel_pb";
import { inboxTransitions } from "../model/typeinfo";
import {
  aSubmittedVacation,
  aTicket,
  item,
  itemName,
  refusal,
  sprint2,
  story2,
  ticket,
  vacation,
} from "../test/fixtures";
import { TransitionPanel } from "./TransitionPanel";

const answer = (outcome: TransitionOutcome, reason = "", hold = "") =>
  create(TransitionItemResponseSchema, { outcome, reason, hold });
const applied = () => vi.fn().mockResolvedValue(answer(TransitionOutcome.APPLIED));

// The list item that holds a move's button and its rules.
const row = (to: string) => screen.getByRole("button", { name: `Move to ${to}` }).closest("li")!;

describe("what is offered", () => {
  test("a button per transition leaving the current state, and no other", () => {
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={applied()} />);
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Move to open",
      "Move to approved",
      "Move to resolved",
    ]);
  });

  test("a state nothing leaves says so", () => {
    const canceled = item({ name: itemName(4), type: ticket.name, state: "canceled", category: StateCategory.CANCELED });
    render(<TransitionPanel type={ticket} item={canceled} onTransition={applied()} />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByText(/no transition leaves canceled/i)).toBeInTheDocument();
  });

  test("the rules are shown as the Type states them, not evaluated", () => {
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={applied()} />);
    const approved = row("approved");
    expect(approved).toHaveTextContent("allowed: role:approver");
    expect(approved).toHaveTextContent("guard: f_on_hold != true");
    expect(approved).toHaveTextContent("required: f_customer_id, f_amount");
    // No role is known here, and the button is offered all the same.
    expect(within(approved).getByRole("button")).toBeEnabled();
    expect(row("open")).not.toHaveTextContent(/allowed|guard|required/);
  });

  test("except and clears are shown too", () => {
    render(<TransitionPanel type={vacation} item={aSubmittedVacation} onTransition={applied()} />);
    expect(row("approved")).toHaveTextContent("allowed: field:f_approver");
    expect(row("approved")).toHaveTextContent("except: creator");
    const resolved = item({ name: itemName(4), type: ticket.name, state: "resolved", fields: { f_resolution: "fixed" } });
    render(<TransitionPanel type={ticket} item={resolved} onTransition={applied()} />);
    expect(row("open")).toHaveTextContent("clears: f_resolution");
  });

  test("a move only the system can make is greyed out and says why", async () => {
    const onTransition = applied();
    render(<TransitionPanel type={vacation} item={aSubmittedVacation} onTransition={onTransition} />);
    const expired = screen.getByRole("button", { name: "Move to expired" });
    expect(expired).toBeDisabled();
    expect(row("expired")).toHaveTextContent(/only the system can do this/i);
    await userEvent.click(expired);
    expect(onTransition).not.toHaveBeenCalled();
    // The other moves of the same state are offered.
    expect(screen.getByRole("button", { name: "Move to approved" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Move to withdrawn" })).toBeEnabled();
  });

  test("`only` limits the panel to some transitions (an inbox row)", () => {
    render(
      <TransitionPanel
        type={vacation}
        item={aSubmittedVacation}
        only={inboxTransitions(vacation, aSubmittedVacation)}
        onTransition={applied()}
      />,
    );
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual(["Move to approved", "Move to rejected"]);
  });

  test("an open hold is shown: its check and the state asked for", () => {
    const held = item({
      name: aTicket.name,
      type: ticket.name,
      state: "in_progress",
      hold: { name: `${aTicket.name}/holds/h1`, check: "erp_check", fromState: "in_progress", requestedState: "approved" },
    });
    render(<TransitionPanel type={ticket} item={held} onTransition={applied()} />);
    const note = screen.getByText(/held/i);
    expect(note).toHaveTextContent("erp_check");
    expect(note).toHaveTextContent("approved");
  });
});

describe("making a move", () => {
  test("a move that requires nothing is asked at once, with a request ID", async () => {
    const onTransition = applied();
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={onTransition} />);
    await userEvent.click(screen.getByRole("button", { name: "Move to open" }));
    expect(onTransition).toHaveBeenCalledTimes(1);
    const [to, fields, requestId] = onTransition.mock.calls[0]!;
    expect(to).toBe("open");
    expect(fields).toEqual({});
    expect(requestId).toMatch(/\S{8,}/);
    expect(await screen.findByRole("status")).toHaveTextContent("APPLIED");
  });

  test("each move asked is a new request", async () => {
    const onTransition = applied();
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={onTransition} />);
    await userEvent.click(screen.getByRole("button", { name: "Move to open" }));
    await userEvent.click(screen.getByRole("button", { name: "Move to open" }));
    expect(onTransition).toHaveBeenCalledTimes(2);
    expect(onTransition.mock.calls[0]![2]).not.toBe(onTransition.mock.calls[1]![2]);
  });

  test("a move with required fields asks for them first, by field type", async () => {
    const onTransition = applied();
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={onTransition} />);
    await userEvent.click(screen.getByRole("button", { name: "Move to resolved" }));
    expect(onTransition).not.toHaveBeenCalled();
    const resolution = screen.getByRole("combobox", { name: "f_resolution" });
    // Only the required fields: not the whole Item.
    expect(screen.queryByLabelText("f_title")).not.toBeInTheDocument();
    await userEvent.selectOptions(resolution, "fixed");
    await userEvent.click(screen.getByRole("button", { name: "Confirm move to resolved" }));
    expect(onTransition).toHaveBeenCalledTimes(1);
    expect(onTransition.mock.calls[0]!.slice(0, 2)).toEqual(["resolved", { f_resolution: "fixed" }]);
  });

  test("required fields start from the Item's values; only changed ones are sent", async () => {
    const onTransition = applied();
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={onTransition} />);
    await userEvent.click(screen.getByRole("button", { name: "Move to approved" }));
    expect(screen.getByLabelText("f_customer_id")).toHaveValue("cust-good");
    await userEvent.type(screen.getByLabelText("f_amount"), "100");
    await userEvent.click(screen.getByRole("button", { name: "Confirm move to approved" }));
    expect(onTransition.mock.calls[0]!.slice(0, 2)).toEqual(["approved", { f_amount: "100" }]);
  });

  test("the form does not insist: an unfilled required field is the server's to refuse", async () => {
    const onTransition = vi.fn().mockRejectedValue(refusal(Code.FailedPrecondition, "MISSING_FIELDS", { fields: "f_resolution" }));
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={onTransition} />);
    await userEvent.click(screen.getByRole("button", { name: "Move to resolved" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm move to resolved" }));
    expect(onTransition).toHaveBeenCalledWith("resolved", {}, expect.any(String));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("FAILED_PRECONDITION");
    expect(alert).toHaveTextContent("MISSING_FIELDS");
    expect(alert).toHaveTextContent("fields: f_resolution");
  });
});

describe("the server's answer", () => {
  test("a refusal is shown with its code and reason", async () => {
    const onTransition = vi
      .fn()
      .mockRejectedValue(refusal(Code.FailedPrecondition, "GUARD_FAILED", { from_state: "in_progress", to_state: "open" }));
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={onTransition} />);
    await userEvent.click(screen.getByRole("button", { name: "Move to open" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("FAILED_PRECONDITION");
    expect(alert).toHaveTextContent("GUARD_FAILED");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  test("DENIED is an answer, not an error: shown with its reason", async () => {
    const onTransition = vi.fn().mockResolvedValue(answer(TransitionOutcome.DENIED, "CUSTOMER_ON_HOLD"));
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={onTransition} />);
    await userEvent.click(screen.getByRole("button", { name: "Move to open" }));
    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("DENIED");
    expect(status).toHaveTextContent("CUSTOMER_ON_HOLD");
  });

  test("ERROR is shown with its reason", async () => {
    const onTransition = vi.fn().mockResolvedValue(answer(TransitionOutcome.ERROR, "CHECK_UNAVAILABLE"));
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={onTransition} />);
    await userEvent.click(screen.getByRole("button", { name: "Move to open" }));
    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("ERROR");
    expect(status).toHaveTextContent("CHECK_UNAVAILABLE");
  });

  test("PENDING offers to ask again, with the same request ID", async () => {
    const onTransition = vi
      .fn()
      .mockResolvedValueOnce(answer(TransitionOutcome.PENDING, "", `${aTicket.name}/holds/h1`))
      .mockResolvedValueOnce(answer(TransitionOutcome.APPLIED));
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={onTransition} />);
    await userEvent.click(screen.getByRole("button", { name: "Move to open" }));
    expect(await screen.findByRole("status")).toHaveTextContent("PENDING");
    await userEvent.click(screen.getByRole("button", { name: "Check again" }));
    expect(onTransition).toHaveBeenCalledTimes(2);
    const [first, second] = onTransition.mock.calls;
    expect(second).toEqual(first); // the same state, patch and request ID
    expect(await screen.findByRole("status")).toHaveTextContent("APPLIED");
    expect(screen.queryByRole("button", { name: "Check again" })).not.toBeInTheDocument();
  });
});

// Jira part 2, step 1: a Type says what a state will do by itself and what
// a move computes. Shown as text, like every other rule; nothing is
// evaluated here.
describe("timers and computed effects", () => {
  test("a move the state's timer makes at a field's instant says so, and stays offered", () => {
    const active = item({ name: itemName(5), type: sprint2.name, state: "active", category: StateCategory.ACTIVE });
    render(<TransitionPanel type={sprint2} item={active} onTransition={applied()} />);
    expect(row("closed")).toHaveTextContent("made by the timer at f_end_at");
    // A person may still close by hand: the transition has no allowed list.
    expect(within(row("closed")).getByRole("button")).toBeEnabled();
    expect(row("review")).not.toHaveTextContent(/timer/);
  });

  test("a move the state's timer makes after a duration says so", () => {
    const review = item({ name: itemName(6), type: sprint2.name, state: "review", category: StateCategory.ACTIVE });
    render(<TransitionPanel type={sprint2} item={review} onTransition={applied()} />);
    expect(row("closed")).toHaveTextContent("made by the timer after 48h");
  });

  test("computed effects are shown as the Type states them", () => {
    const todo = item({ name: itemName(7), type: story2.name, state: "todo", category: StateCategory.TODO });
    render(<TransitionPanel type={story2} item={todo} onTransition={applied()} />);
    expect(row("done")).toHaveTextContent("sets: f_resolved_at = cause.at");
    expect(row("done")).toHaveTextContent("required: f_resolution");

    const done = item({ name: itemName(8), type: story2.name, state: "done", category: StateCategory.DONE });
    render(<TransitionPanel type={story2} item={done} onTransition={applied()} />);
    expect(row("todo")).toHaveTextContent("sets: f_resolved_at = null");
    expect(row("todo")).toHaveTextContent("clears: f_resolution");
  });

  test("a move without effects or timers says nothing of them", () => {
    render(<TransitionPanel type={ticket} item={aTicket} onTransition={applied()} />);
    expect(row("open")).not.toHaveTextContent(/sets:|timer/);
  });
});
