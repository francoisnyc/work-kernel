// Moves as buttons; required fields asked for first; refusals in words.
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { ErrorInfoSchema } from "@gen/google/rpc/error_details_pb";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { MoveButtons } from "./MoveButtons";

const moves = [
  { to: "waiting_for_customer", label: "Wait for customer", required: [] },
  { to: "resolved", label: "Resolve", required: ["f_resolution"] },
];
const options = { f_resolution: ["fixed", "workaround", "wontfix", "duplicate"] };

test("a move without required fields is asked for at once", async () => {
  const onMove = vi.fn(async () => {});
  render(<MoveButtons moves={moves} options={options} onMove={onMove} />);
  await userEvent.click(screen.getByRole("button", { name: "Wait for customer" }));
  expect(onMove).toHaveBeenCalledWith("waiting_for_customer", undefined);
});

test("a move that requires a field asks for it in a dialog first", async () => {
  const onMove = vi.fn(async () => {});
  render(<MoveButtons moves={moves} options={options} onMove={onMove} />);
  await userEvent.click(screen.getByRole("button", { name: "Resolve" }));
  expect(onMove).not.toHaveBeenCalled();
  const dialog = screen.getByRole("dialog", { name: "Resolve" });
  const select = within(dialog).getByRole("combobox", { name: "Resolution" });
  expect(within(select).getAllByRole("option").map((o) => o.textContent)).toEqual(["Choose…", "Fixed", "Workaround", "Won't fix", "Duplicate"]);
  expect(within(dialog).getByRole("button", { name: "Resolve" })).toBeDisabled();
  await userEvent.selectOptions(select, "fixed");
  await userEvent.click(within(dialog).getByRole("button", { name: "Resolve" }));
  expect(onMove).toHaveBeenCalledWith("resolved", { f_resolution: "fixed" });
});

test("canceling the dialog moves nothing", async () => {
  const onMove = vi.fn(async () => {});
  render(<MoveButtons moves={moves} options={options} onMove={onMove} />);
  await userEvent.click(screen.getByRole("button", { name: "Resolve" }));
  await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(onMove).not.toHaveBeenCalled();
});

test("the server's refusal is shown in plain words", async () => {
  const refused = new ConnectError("x", Code.FailedPrecondition, undefined, [
    { desc: ErrorInfoSchema, value: create(ErrorInfoSchema, { reason: "GUARD_FAILED", domain: "pli.francoisnyc.github.io" }) },
  ]);
  render(
    <MoveButtons
      moves={[{ to: "done", label: "Mark done", required: [] }]}
      options={{}}
      onMove={vi.fn(async () => Promise.reject(refused))}
      ctx={{ guard: "An epic is done only when all its issues are done or canceled." }}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "Mark done" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("An epic is done only when all its issues are done or canceled.");
});

test("no moves: says so", () => {
  render(<MoveButtons moves={[]} options={{}} onMove={vi.fn()} />);
  expect(screen.getByText("No moves from here.")).toBeInTheDocument();
});
