// A person's initials in a circle, coloured by their ID. Decorative: the
// name is always written beside it.
import type { ReactElement } from "react";
import { initials } from "../model/people";

const hues = [222, 160, 28, 280, 340, 190, 100];

export function Avatar({ principal, size = 24 }: { principal: string; size?: number }): ReactElement {
  let h = 0;
  for (const c of principal) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  const hue = hues[h % hues.length]!;
  return (
    <span className="avatar" aria-hidden="true" style={{ width: size, height: size, ["--hue" as string]: hue }}>
      {initials(principal)}
    </span>
  );
}
