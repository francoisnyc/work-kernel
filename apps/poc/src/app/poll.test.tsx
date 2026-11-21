// Modest polling: only a visible tab reads again.
import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { useNow, usePoll } from "./poll";

let visibility: DocumentVisibilityState = "visible";

function setVisibility(v: DocumentVisibilityState) {
  visibility = v;
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  vi.useFakeTimers();
  visibility = "visible";
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("usePoll", () => {
  test("reloads every interval while visible, and not on mount", () => {
    const reload = vi.fn();
    renderHook(() => usePoll(reload, 30_000));
    expect(reload).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(30_000));
    expect(reload).toHaveBeenCalledTimes(1);
    act(() => vi.advanceTimersByTime(60_000));
    expect(reload).toHaveBeenCalledTimes(3);
  });

  test("not while hidden; at once on coming back after a missed turn", () => {
    const reload = vi.fn();
    renderHook(() => usePoll(reload, 30_000));
    act(() => setVisibility("hidden"));
    act(() => vi.advanceTimersByTime(90_000));
    expect(reload).not.toHaveBeenCalled();
    act(() => setVisibility("visible"));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  test("coming back before a turn was missed reads nothing extra", () => {
    const reload = vi.fn();
    renderHook(() => usePoll(reload, 30_000));
    act(() => setVisibility("hidden"));
    act(() => vi.advanceTimersByTime(10_000));
    act(() => setVisibility("visible"));
    expect(reload).not.toHaveBeenCalled();
  });

  test("not while paused (an editor is open)", () => {
    const reload = vi.fn();
    const { rerender } = renderHook(({ paused }) => usePoll(reload, 30_000, paused), { initialProps: { paused: true } });
    act(() => vi.advanceTimersByTime(90_000));
    expect(reload).not.toHaveBeenCalled();
    rerender({ paused: false });
    act(() => vi.advanceTimersByTime(30_000));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  test("stops when the page goes away", () => {
    const reload = vi.fn();
    const { unmount } = renderHook(() => usePoll(reload, 30_000));
    unmount();
    act(() => vi.advanceTimersByTime(90_000));
    expect(reload).not.toHaveBeenCalled();
  });
});

test("useNow ticks without reading anything", () => {
  vi.setSystemTime(new Date("2026-11-21T12:00:00Z"));
  const { result } = renderHook(() => useNow(30_000));
  expect(result.current.toISOString()).toBe("2026-11-21T12:00:00.000Z");
  act(() => vi.advanceTimersByTime(30_000));
  expect(result.current.toISOString()).toBe("2026-11-21T12:00:30.000Z");
});
