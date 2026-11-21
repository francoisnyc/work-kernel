// A list of facts: each value is labelled by its term, so it can be found
// by its name ("Status", "Requester").
import { useId, type ReactElement, type ReactNode } from "react";

export function Fact({ term, children, labelFor }: { term: string; children: ReactNode; labelFor?: string }): ReactElement {
  const id = useId();
  return (
    <>
      <dt id={id}>{labelFor ? <label htmlFor={labelFor}>{term}</label> : term}</dt>
      {labelFor ? <dd>{children}</dd> : <dd aria-labelledby={id}>{children}</dd>}
    </>
  );
}
