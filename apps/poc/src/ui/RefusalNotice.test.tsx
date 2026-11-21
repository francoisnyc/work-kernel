// A refusal: plain words first, the server's answer behind Details.
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { ErrorInfoSchema } from "@gen/google/rpc/error_details_pb";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { RefusalNotice } from "./RefusalNotice";

const guardFailed = new ConnectError("the transition's guard does not hold", Code.FailedPrecondition, undefined, [
  {
    desc: ErrorInfoSchema,
    value: create(ErrorInfoSchema, {
      reason: "GUARD_FAILED",
      domain: "pli.francoisnyc.github.io",
      metadata: { from_state: "in_progress", to_state: "done" },
    }),
  },
]);

test("the sentence, with the code, reason and metadata behind Details", () => {
  render(<RefusalNotice error={guardFailed} ctx={{ guard: "An epic is done only when all its issues are done or canceled." }} />);
  const alert = screen.getByRole("alert");
  expect(alert).toHaveTextContent("An epic is done only when all its issues are done or canceled.");
  const details = screen.getByText("Details").closest("details")!;
  expect(details).not.toHaveAttribute("open");
  expect(details).toHaveTextContent("FAILED_PRECONDITION");
  expect(details).toHaveTextContent("GUARD_FAILED");
  expect(details).toHaveTextContent("to_state: done");
});

test("nothing to say: nothing rendered", () => {
  const { container } = render(<RefusalNotice error={undefined} />);
  expect(container).toBeEmptyDOMElement();
});
