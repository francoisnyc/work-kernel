// The form is built from the Type alone: the same component renders a
// ticket, a vacation request and a leave balance.
import { Code } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { aSubmittedVacation, aTicket, item, itemName, leaveBalance, refusal, story, ticket, vacation } from "../test/fixtures";
import { ItemForm } from "./ItemForm";

const ok = () => vi.fn().mockResolvedValue(undefined);

describe("create", () => {
  test("one input per declared field, for any Type", () => {
    const { unmount } = render(<ItemForm type={vacation} submitLabel="Create" onSubmit={ok()} />);
    for (const f of vacation.fields) expect(screen.getByLabelText(f.id)).toBeInTheDocument();
    unmount();
    render(<ItemForm type={story} submitLabel="Create" onSubmit={ok()} />);
    for (const f of story.fields) expect(screen.getByLabelText(f.id)).toBeInTheDocument();
    expect(screen.queryByLabelText("f_start")).not.toBeInTheDocument();
  });

  test("a kernel-written field is shown, never editable; nothing is frozen yet", () => {
    render(<ItemForm type={vacation} submitLabel="Create" onSubmit={ok()} />);
    expect(screen.getByLabelText("f_approver")).toBeDisabled();
    expect(screen.getByLabelText("f_start")).toBeEnabled();
  });

  test("sends only the fields filled in", async () => {
    const onSubmit = ok();
    render(<ItemForm type={vacation} submitLabel="Create" onSubmit={onSubmit} />);
    await userEvent.type(screen.getByLabelText("f_days"), "3");
    await userEvent.selectOptions(screen.getByLabelText("f_leave_type"), "annual");
    await userEvent.type(screen.getByLabelText("f_comment"), "skiing");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(onSubmit).toHaveBeenCalledWith({ f_days: "3", f_leave_type: "annual", f_comment: "skiing" });
  });

  test("an empty form can be created: no field is required by a form", async () => {
    const onSubmit = ok();
    render(<ItemForm type={ticket} submitLabel="Create" onSubmit={onSubmit} />);
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    expect(onSubmit).toHaveBeenCalledWith({});
  });

  test("a role-written field is editable; the server's refusal is shown and the form keeps its values", async () => {
    const onSubmit = vi.fn().mockRejectedValue(refusal(Code.PermissionDenied, "FIELD_NOT_WRITABLE", { fields: "f_allowance" }));
    render(<ItemForm type={leaveBalance} submitLabel="Create" onSubmit={onSubmit} />);
    const allowance = screen.getByLabelText("f_allowance");
    expect(allowance).toBeEnabled();
    expect(screen.getByLabelText("f_used")).toBeDisabled();
    await userEvent.type(allowance, "25");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("PERMISSION_DENIED");
    expect(alert).toHaveTextContent("FIELD_NOT_WRITABLE");
    expect(alert).toHaveTextContent("f_allowance");
    expect(allowance).toHaveValue("25");
  });
});

describe("edit", () => {
  test("starts from the Item's values", () => {
    render(<ItemForm type={ticket} item={aTicket} submitLabel="Save" onSubmit={ok()} />);
    expect(screen.getByLabelText("f_title")).toHaveValue("printer on fire");
    expect(screen.getByLabelText("f_priority")).toHaveValue("2");
    expect(screen.getByLabelText("f_urgent")).toBeChecked();
    expect(screen.getByLabelText("f_assignee")).toHaveValue("");
  });

  test("the fields its state freezes are read-only; the others are not", () => {
    render(<ItemForm type={vacation} item={aSubmittedVacation} submitLabel="Save" onSubmit={ok()} />);
    for (const id of ["f_start", "f_end", "f_days", "f_leave_type", "f_approver"]) {
      expect(screen.getByLabelText(id), id).toBeDisabled();
    }
    expect(screen.getByLabelText("f_start")).toHaveAccessibleDescription(/frozen in state submitted/i);
    expect(screen.getByLabelText("f_approver")).toHaveAccessibleDescription(/written by the kernel/i);
    expect(screen.getByLabelText("f_comment")).toBeEnabled();
  });

  test("the same Type in a state that freezes nothing is editable", () => {
    const draft = item({ name: itemName(12), type: vacation.name, state: "draft", fields: { f_days: "2" } });
    render(<ItemForm type={vacation} item={draft} submitLabel="Save" onSubmit={ok()} />);
    expect(screen.getByLabelText("f_start")).toBeEnabled();
    expect(screen.getByLabelText("f_approver")).toBeDisabled();
  });

  test("nothing to save until something changes", async () => {
    render(<ItemForm type={ticket} item={aTicket} submitLabel="Save" onSubmit={ok()} />);
    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();
    await userEvent.type(screen.getByLabelText("f_title"), "!");
    expect(save).toBeEnabled();
  });

  test("sends only the changed fields, an emptied one as null", async () => {
    const onSubmit = ok();
    render(<ItemForm type={ticket} item={aTicket} submitLabel="Save" onSubmit={onSubmit} />);
    await userEvent.type(screen.getByLabelText("f_assignee"), "user:bob");
    await userEvent.clear(screen.getByLabelText("f_reporter"));
    await userEvent.click(screen.getByLabelText("f_urgent"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(onSubmit).toHaveBeenCalledWith({ f_assignee: "user:bob", f_reporter: null, f_urgent: false });
  });

  test("a refused save shows the refusal", async () => {
    const onSubmit = vi.fn().mockRejectedValue(refusal(Code.Aborted, "STALE"));
    render(<ItemForm type={ticket} item={aTicket} submitLabel="Save" onSubmit={onSubmit} />);
    await userEvent.type(screen.getByLabelText("f_title"), "!");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("ABORTED");
    expect(alert).toHaveTextContent("STALE");
    expect(screen.getByLabelText("f_title")).toHaveValue("printer on fire!");
  });
});
