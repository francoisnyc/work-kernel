import { Code, ConnectError } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { BadRequestSchema, ErrorInfoSchema } from "../gen/google/rpc/error_details_pb";
import { refusal } from "../test/fixtures";
import { RefusalView } from "./RefusalView";

test("shows the code, the reason and the metadata", () => {
  render(<RefusalView error={refusal(Code.FailedPrecondition, "FIELD_FROZEN", { fields: "f_start", state: "submitted" })} />);
  const alert = screen.getByRole("alert");
  expect(alert).toHaveTextContent("FAILED_PRECONDITION");
  expect(alert).toHaveTextContent("FIELD_FROZEN");
  expect(alert).toHaveTextContent("fields: f_start");
  expect(alert).toHaveTextContent("state: submitted");
  expect(alert).toHaveTextContent("the fixed message of this reason");
});

test("shows field violations", () => {
  const err = new ConnectError("invalid request", Code.InvalidArgument, undefined, [
    { desc: ErrorInfoSchema, value: { reason: "INVALID_REQUEST", domain: "pli.francoisnyc.github.io" } },
    { desc: BadRequestSchema, value: { fieldViolations: [{ field: "fields.f_amount", description: "want a whole number" }] } },
  ]);
  render(<RefusalView error={err} />);
  expect(screen.getByRole("alert")).toHaveTextContent("fields.f_amount: want a whole number");
});

test("an error that is not the server's still shows", () => {
  render(<RefusalView error={new TypeError("Failed to fetch")} />);
  expect(screen.getByRole("alert")).toHaveTextContent("Failed to fetch");
});
