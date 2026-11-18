// One input per declared field type; the same code for every Type.
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, test, vi } from "vitest";
import type { Type } from "../gen/kernel/v1/kernel_pb";
import type { FieldAccess } from "../model/typeinfo";
import { leaveBalance, ticket, vacation } from "../test/fixtures";
import { FieldInput } from "./FieldInput";

const editable: FieldAccess = { mode: "editable", roles: [] };

// Holds the value, as a form would, and reports each change.
function Harness(props: {
  type: Type;
  id: string;
  initial: string | boolean;
  access?: FieldAccess;
  state?: string;
  principals?: string[];
  onChange?: (v: string | boolean) => void;
}) {
  const [value, setValue] = useState(props.initial);
  const field = props.type.fields.find((f) => f.id === props.id)!;
  return (
    <FieldInput
      field={field}
      value={value}
      access={props.access ?? editable}
      state={props.state}
      principals={props.principals}
      onChange={(v) => {
        setValue(v);
        props.onChange?.(v);
      }}
    />
  );
}

describe("by field type", () => {
  test("string: a text box labelled with the field ID", async () => {
    const onChange = vi.fn();
    render(<Harness type={ticket} id="f_title" initial="" onChange={onChange} />);
    const input = screen.getByLabelText("f_title");
    expect(input).toHaveAttribute("type", "text");
    await userEvent.type(input, "hi");
    expect(onChange).toHaveBeenLastCalledWith("hi");
    expect(input).toHaveValue("hi");
  });

  test("int: text with a numeric keyboard (an int64 does not fit a number input)", async () => {
    const onChange = vi.fn();
    render(<Harness type={ticket} id="f_amount" initial="9007199254740993" onChange={onChange} />);
    const input = screen.getByLabelText("f_amount");
    expect(input).toHaveAttribute("type", "text");
    expect(input).toHaveAttribute("inputmode", "numeric");
    expect(input).toHaveValue("9007199254740993");
    await userEvent.type(input, "1");
    expect(onChange).toHaveBeenLastCalledWith("90071992547409931");
  });

  test("bool: a checkbox", async () => {
    const onChange = vi.fn();
    render(<Harness type={ticket} id="f_urgent" initial={false} onChange={onChange} />);
    const box = screen.getByRole("checkbox", { name: "f_urgent" });
    expect(box).not.toBeChecked();
    await userEvent.click(box);
    expect(onChange).toHaveBeenLastCalledWith(true);
    expect(box).toBeChecked();
  });

  test("enum: a select of the declared options, with an empty choice", async () => {
    const onChange = vi.fn();
    render(<Harness type={ticket} id="f_resolution" initial="" onChange={onChange} />);
    const select = screen.getByRole("combobox", { name: "f_resolution" });
    expect(within(select).getAllByRole("option").map((o) => o.getAttribute("value"))).toEqual([
      "",
      "fixed",
      "wontfix",
      "duplicate",
    ]);
    await userEvent.selectOptions(select, "wontfix");
    expect(onChange).toHaveBeenLastCalledWith("wontfix");
  });

  test("date: a date input holding an ISO date", () => {
    render(<Harness type={vacation} id="f_start" initial="2026-12-01" />);
    const input = screen.getByLabelText("f_start");
    expect(input).toHaveAttribute("type", "date");
    expect(input).toHaveValue("2026-12-01");
  });

  test("principal: a text box that suggests the known principals", async () => {
    const onChange = vi.fn();
    render(<Harness type={ticket} id="f_assignee" initial="" principals={["user:ana", "user:bob"]} onChange={onChange} />);
    const input = screen.getByLabelText("f_assignee");
    expect(input).toHaveAttribute("type", "text");
    const list = document.getElementById(input.getAttribute("list") ?? "");
    expect(list?.tagName).toBe("DATALIST");
    expect([...list!.querySelectorAll("option")].map((o) => o.value)).toEqual(["user:ana", "user:bob"]);
    // A principal outside the suggestions can still be typed.
    await userEvent.type(input, "agent:triage-bot");
    expect(onChange).toHaveBeenLastCalledWith("agent:triage-bot");
  });
});

describe("by who may write", () => {
  test("a kernel-written field is disabled and says so", () => {
    render(<Harness type={vacation} id="f_approver" initial="user:bob" access={{ mode: "kernel", roles: [] }} />);
    const input = screen.getByLabelText("f_approver");
    expect(input).toBeDisabled();
    expect(input).toHaveValue("user:bob");
    expect(input).toHaveAccessibleDescription(/written by the kernel/i);
  });

  test("a frozen field is disabled and names the state", () => {
    render(
      <Harness type={vacation} id="f_start" initial="2026-12-01" access={{ mode: "frozen", roles: [] }} state="submitted" />,
    );
    const input = screen.getByLabelText("f_start");
    expect(input).toBeDisabled();
    expect(input).toHaveAccessibleDescription(/frozen in state submitted/i);
  });

  test("every kind of input can be disabled", () => {
    const frozen: FieldAccess = { mode: "frozen", roles: [] };
    render(
      <>
        <Harness type={ticket} id="f_urgent" initial={true} access={frozen} state="resolved" />
        <Harness type={ticket} id="f_resolution" initial="fixed" access={frozen} state="resolved" />
        <Harness type={ticket} id="f_amount" initial="1" access={frozen} state="resolved" />
        <Harness type={ticket} id="f_assignee" initial="user:bob" access={frozen} state="resolved" />
      </>,
    );
    for (const id of ["f_urgent", "f_resolution", "f_amount", "f_assignee"]) {
      expect(screen.getByLabelText(id), id).toBeDisabled();
    }
  });

  test("a role-written field stays enabled and names its roles", () => {
    render(<Harness type={leaveBalance} id="f_allowance" initial="" access={{ mode: "editable", roles: ["hr_admin"] }} />);
    const input = screen.getByLabelText("f_allowance");
    expect(input).toBeEnabled();
    expect(input).toHaveAccessibleDescription(/written by role hr_admin/i);
  });

  test("an ordinary field has no hint", () => {
    render(<Harness type={ticket} id="f_title" initial="" />);
    expect(screen.getByLabelText("f_title")).toHaveAccessibleDescription("");
  });
});
