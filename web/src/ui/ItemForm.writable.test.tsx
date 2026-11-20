// The etag step, in the form: a save sends what the user changed and
// nothing the user cannot write, even when the form holds an older value
// of a field only the kernel writes (the page read the Item again and the
// form was not rebuilt).
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { item, itemName, leaveBalance, story2 } from "../test/fixtures";
import { ItemForm } from "./ItemForm";

const ok = () => vi.fn().mockResolvedValue(undefined);

test("the kernel wrote a field after the form was built: the save sends the user's change only", async () => {
  const onSubmit = ok();
  const opened = item({ name: itemName(41), type: story2.name, state: "todo", etag: "c1", fields: { f_summary: "Log in" } });
  const { rerender } = render(<ItemForm type={story2} item={opened} submitLabel="Save" onSubmit={onSubmit} />);
  // The same form (no new key), the Item as it is now.
  const now = item({ ...opened, fields: { f_summary: "Log in", f_resolved_at: "2026-11-19T17:00:00.123456Z" } });
  rerender(<ItemForm type={story2} item={now} submitLabel="Save" onSubmit={onSubmit} />);

  // The kernel's write alone is nothing to save.
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  await userEvent.type(screen.getByLabelText("f_summary"), " with SSO");
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(onSubmit).toHaveBeenCalledTimes(1);
  expect(onSubmit).toHaveBeenCalledWith({ f_summary: "Log in with SSO" });
});

test("a role-written field typed by a user without the role is not sent", async () => {
  const onSubmit = ok();
  const balance = item({
    name: itemName(42),
    type: leaveBalance.name,
    state: "open",
    etag: "c1",
    fields: { f_owner: "user:ana", f_allowance: "25", f_used: "3" },
  });
  const { unmount } = render(<ItemForm type={leaveBalance} item={balance} roles={["approver"]} submitLabel="Save" onSubmit={onSubmit} />);
  await userEvent.type(screen.getByLabelText("f_allowance"), "0");
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  unmount();

  // A holder of the role sends it.
  render(<ItemForm type={leaveBalance} item={balance} roles={["hr_admin"]} submitLabel="Save" onSubmit={onSubmit} />);
  await userEvent.type(screen.getByLabelText("f_allowance"), "0");
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(onSubmit).toHaveBeenCalledWith({ f_allowance: "250" });
});

test("a role-written field is read-only for a user without the role, and says which role writes it", () => {
  const balance = item({ name: itemName(43), type: leaveBalance.name, state: "open", etag: "c1", fields: { f_allowance: "25" } });
  const { unmount } = render(<ItemForm type={leaveBalance} item={balance} roles={["approver"]} submitLabel="Save" onSubmit={ok()} />);
  expect(screen.getByLabelText("f_allowance")).toBeDisabled();
  expect(screen.getByLabelText("f_allowance")).toHaveAccessibleDescription(/written by role hr_admin only/i);
  unmount();
  render(<ItemForm type={leaveBalance} item={balance} roles={["hr_admin"]} submitLabel="Save" onSubmit={ok()} />);
  expect(screen.getByLabelText("f_allowance")).toBeEnabled();
});
