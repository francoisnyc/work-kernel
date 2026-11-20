// One input per declared field type; the same code for every Type.
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, test, vi } from "vitest";
import type { Type } from "../gen/kernel/v1/kernel_pb";
import type { FieldAccess } from "../model/typeinfo";
import type { FormValue } from "../model/values";
import { leaveBalance, sprint2, story2, story3, ticket, vacation } from "../test/fixtures";
import { FieldInput } from "./FieldInput";

const editable: FieldAccess = { mode: "editable", roles: [] };

// Holds the value, as a form would, and reports each change.
function Harness(props: {
  type: Type;
  id: string;
  initial: FormValue;
  access?: FieldAccess;
  state?: string;
  principals?: string[];
  onChange?: (v: FormValue) => void;
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

  // The stored instant is UTC; the form holds it on the browser's clock
  // (model/values.ts), and that is what this input is given.
  test("datetime: a datetime-local input, to the second, with the UTC instant as its title", () => {
    const zone = process.env.TZ;
    process.env.TZ = "America/New_York";
    try {
      // A second that is not :00: a datetime-local input drops zero seconds.
      render(<Harness type={sprint2} id="f_end_at" initial="2026-12-04T12:00:07" />);
      const input = screen.getByLabelText("f_end_at");
      expect(input).toHaveAttribute("type", "datetime-local");
      expect(input).toHaveAttribute("step", "1");
      expect(input).toHaveValue("2026-12-04T12:00:07");
      expect(input).toHaveAttribute("title", "2026-12-04T17:00:07.000Z");
    } finally {
      if (zone === undefined) delete process.env.TZ;
      else process.env.TZ = zone;
    }
  });

  test("datetime: a change is reported as the input's local text", async () => {
    const onChange = vi.fn();
    render(<Harness type={sprint2} id="f_end_at" initial="" onChange={onChange} />);
    const input = screen.getByLabelText("f_end_at");
    expect(input).not.toHaveAttribute("title");
    await userEvent.type(input, "2026-12-11T09:30:07");
    expect(onChange).toHaveBeenLastCalledWith("2026-12-11T09:30:07");
  });

  test("datetime: a kernel-written instant is shown, disabled", () => {
    render(<Harness type={story2} id="f_resolved_at" initial="2026-12-04T12:00:00" access={{ mode: "kernel", roles: [] }} />);
    const input = screen.getByLabelText("f_resolved_at");
    expect(input).toHaveAttribute("type", "datetime-local");
    expect(input).toBeDisabled();
    expect(input).toHaveAccessibleDescription("written by the kernel");
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

// Jira part 2, step 2: a multi-value field is a set. Its members are chips;
// one more input adds a member. The form holds the members, sorted; what is
// sent is worked out in model/values.ts.
describe("a multi-value field", () => {
  const chips = (id: string) =>
    within(screen.getByRole("group", { name: id }))
      .queryAllByRole("listitem")
      .map((li) => li.textContent?.replace(/Remove.*/, "").trim());

  test("its members are chips, each with a remove button; a text box adds one", async () => {
    const onChange = vi.fn();
    render(<Harness type={story3} id="f_labels" initial={["backend", "sso"]} onChange={onChange} />);
    expect(chips("f_labels")).toEqual(["backend", "sso"]);
    const group = within(screen.getByRole("group", { name: "f_labels" }));

    await userEvent.type(group.getByRole("textbox", { name: "Add to f_labels" }), "api");
    await userEvent.click(group.getByRole("button", { name: "Add to f_labels" }));
    // Kept sorted, as the server stores it.
    expect(onChange).toHaveBeenLastCalledWith(["api", "backend", "sso"]);
    expect(chips("f_labels")).toEqual(["api", "backend", "sso"]);
    // The box is ready for the next one.
    expect(group.getByRole("textbox", { name: "Add to f_labels" })).toHaveValue("");

    await userEvent.click(group.getByRole("button", { name: "Remove backend" }));
    expect(onChange).toHaveBeenLastCalledWith(["api", "sso"]);
    expect(chips("f_labels")).toEqual(["api", "sso"]);
  });

  test("Enter adds too, without submitting the form around it", async () => {
    const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
    const onChange = vi.fn();
    render(
      <form onSubmit={onSubmit}>
        <Harness type={story3} id="f_labels" initial={[]} onChange={onChange} />
      </form>,
    );
    await userEvent.type(screen.getByRole("textbox", { name: "Add to f_labels" }), "urgent{Enter}");
    expect(onChange).toHaveBeenLastCalledWith(["urgent"]);
    expect(onSubmit).not.toHaveBeenCalled();
  });

  test("a member already there is not added twice; an empty box adds nothing", async () => {
    const onChange = vi.fn();
    render(<Harness type={story3} id="f_labels" initial={["sso"]} onChange={onChange} />);
    const box = screen.getByRole("textbox", { name: "Add to f_labels" });
    await userEvent.type(box, "sso{Enter}");
    await userEvent.type(box, "{Enter}");
    expect(onChange).not.toHaveBeenCalled();
    expect(chips("f_labels")).toEqual(["sso"]);
  });

  test("at its max, nothing more can be added, and it says so", async () => {
    render(<Harness type={story3} id="f_labels" initial={["a", "b", "c"]} />);
    const group = within(screen.getByRole("group", { name: "f_labels" }));
    expect(group.getByRole("button", { name: "Add to f_labels" })).toBeDisabled();
    expect(screen.getByRole("group", { name: "f_labels" })).toHaveTextContent("at most 3");
    // Removing one makes room.
    await userEvent.click(group.getByRole("button", { name: "Remove a" }));
    expect(group.getByRole("button", { name: "Add to f_labels" })).toBeEnabled();
  });

  test("an enum set: the options not yet chosen, in a select", async () => {
    const onChange = vi.fn();
    render(<Harness type={story3} id="f_colors" initial={["green"]} onChange={onChange} />);
    const group = within(screen.getByRole("group", { name: "f_colors" }));
    const select = group.getByRole("combobox", { name: "Add to f_colors" });
    expect(within(select).getAllByRole("option").map((o) => o.getAttribute("value"))).toEqual(["", "red", "blue"]);
    await userEvent.selectOptions(select, "blue");
    await userEvent.click(group.getByRole("button", { name: "Add to f_colors" }));
    expect(onChange).toHaveBeenLastCalledWith(["blue", "green"]);
  });

  test("a set of principals suggests the known ones; a set of dates takes a date", async () => {
    const onChange = vi.fn();
    render(<Harness type={story3} id="f_reviewers" initial={[]} principals={["user:ana", "user:bob"]} onChange={onChange} />);
    const box = screen.getByRole("combobox", { name: "Add to f_reviewers" });
    const list = document.getElementById(box.getAttribute("list") ?? "");
    expect([...list!.querySelectorAll("option")].map((o) => o.value)).toEqual(["user:ana", "user:bob"]);
    await userEvent.type(box, "user:sam{Enter}");
    expect(onChange).toHaveBeenLastCalledWith(["user:sam"]);

    render(<Harness type={story3} id="f_days" initial={["2026-12-04"]} />);
    expect(within(screen.getByRole("group", { name: "f_days" })).getByLabelText("Add to f_days")).toHaveAttribute("type", "date");
    expect(chips("f_days")).toEqual(["2026-12-04"]);
  });

  test("a set only the kernel writes is shown, and nothing in it can change", () => {
    render(<Harness type={story3} id="f_stamps" initial={["x", "y"]} access={{ mode: "kernel", roles: [] }} />);
    expect(chips("f_stamps")).toEqual(["x", "y"]);
    const group = within(screen.getByRole("group", { name: "f_stamps" }));
    expect(group.queryByRole("button", { name: /Remove/ })).not.toBeInTheDocument();
    expect(group.getByRole("button", { name: "Add to f_stamps" })).toBeDisabled();
    expect(screen.getByRole("group", { name: "f_stamps" })).toHaveTextContent("written by the kernel");
  });

  test("an empty set says so", () => {
    render(<Harness type={story3} id="f_labels" initial={[]} />);
    expect(chips("f_labels")).toEqual([]);
    expect(screen.getByRole("group", { name: "f_labels" })).toHaveTextContent("(none)");
  });
});
