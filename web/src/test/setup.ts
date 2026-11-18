// Adds the DOM matchers (toBeDisabled, toHaveValue...) and unmounts what a
// test rendered before the next one.
import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

afterEach(cleanup);
