// DEV ONLY sign-in: the dev-token file's people, staff and customers apart.
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import type { DevUser } from "../model/devusers";
import { SignIn } from "./SignIn";

const u = (name: string, principal: string, roles: string[] = [], namespace = "acme"): DevUser => ({
  name,
  token: `dev-${name}`,
  principal,
  namespace,
  roles,
});
const users = [u("lea", "user:lea", ["sla_admin", "desk_agent"]), u("sam", "user:sam", ["desk_agent"]), u("kim", "customer:kim"), u("carol", "user:carol", [], "globex")];

test("says it is dev only", () => {
  render(<SignIn users={users} onSignIn={vi.fn()} />);
  expect(screen.getByRole("heading", { level: 1, name: "Sign in" })).toBeInTheDocument();
  expect(screen.getByText(/dev tokens, not real sign-on/i)).toBeInTheDocument();
});

test("staff and customers in two groups, with roles and namespaces", () => {
  render(<SignIn users={users} onSignIn={vi.fn()} />);
  const staff = screen.getByRole("region", { name: "Staff" });
  const customers = screen.getByRole("region", { name: "Customers" });
  expect(within(staff).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual([
    "Sign in as lea",
    "Sign in as sam",
    "Sign in as carol",
  ]);
  expect(within(staff).getByRole("button", { name: "Sign in as lea" })).toHaveTextContent("sla_admin, desk_agent");
  expect(within(staff).getByRole("button", { name: "Sign in as carol" })).toHaveTextContent("globex");
  expect(within(customers).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual(["Sign in as kim"]);
});

test("choosing someone signs in as them", async () => {
  const onSignIn = vi.fn();
  render(<SignIn users={users} onSignIn={onSignIn} />);
  await userEvent.click(screen.getByRole("button", { name: "Sign in as kim" }));
  expect(onSignIn).toHaveBeenCalledWith("kim");
});
