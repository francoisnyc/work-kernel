// A reply and a note are never confused: the tab changes the label, the
// hint and the button, and the kind sent.
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { ErrorInfoSchema } from "@gen/google/rpc/error_details_pb";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { Composer } from "./Composer";

describe("Composer", () => {
  test("a reply to the customer by default, saying who will read it", async () => {
    const onSend = vi.fn(async () => {});
    render(<Composer requester="kim" onSend={onSend} />);
    expect(screen.getByRole("tab", { name: "Reply to customer" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("kim will see this")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: "Reply" }), "We are on it.");
    await userEvent.click(screen.getByRole("button", { name: "Send reply" }));
    expect(onSend).toHaveBeenCalledWith("reply", "We are on it.");
  });

  test("an internal note says so, and is sent as a note", async () => {
    const onSend = vi.fn(async () => {});
    render(<Composer requester="kim" onSend={onSend} />);
    await userEvent.click(screen.getByRole("tab", { name: "Internal note" }));
    expect(screen.getByRole("tab", { name: "Internal note" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Only staff can see this")).toBeInTheDocument();
    expect(screen.queryByText("kim will see this")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send reply" })).not.toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: "Internal note" }), "Check the logs.");
    await userEvent.click(screen.getByRole("button", { name: "Add note" }));
    expect(onSend).toHaveBeenCalledWith("note", "Check the logs.");
  });

  test("nothing to send: the button is disabled", () => {
    render(<Composer requester="kim" onSend={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Send reply" })).toBeDisabled();
  });

  test("sent: the box is emptied", async () => {
    render(<Composer requester="kim" onSend={vi.fn(async () => {})} />);
    const box = screen.getByRole("textbox", { name: "Reply" });
    await userEvent.type(box, "Done.");
    await userEvent.click(screen.getByRole("button", { name: "Send reply" }));
    expect(box).toHaveValue("");
  });

  test("refused: the text stays and the refusal is said in plain words", async () => {
    const refused = new ConnectError("x", Code.PermissionDenied, undefined, [
      { desc: ErrorInfoSchema, value: create(ErrorInfoSchema, { reason: "NOT_ALLOWED", domain: "pli.francoisnyc.github.io" }) },
    ]);
    render(<Composer requester="kim" onSend={vi.fn(async () => Promise.reject(refused))} />);
    const box = screen.getByRole("textbox", { name: "Reply" });
    await userEvent.type(box, "Done.");
    await userEvent.click(screen.getByRole("button", { name: "Send reply" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("You don't have permission to do that.");
    expect(box).toHaveValue("Done.");
  });
});
