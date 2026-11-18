// The search and list forms as requests. The UI builds the filter the user
// asked for and nothing more: whether a field can be filtered or sorted
// across the chosen Types is the server's answer.
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { ValueSchema } from "@bufbuild/protobuf/wkt";
import {
  FieldType,
  type FieldFilterSchema,
  type ItemFilterSchema,
  type LinkDirection,
  type ListItemsRequestSchema,
  type SearchItemsRequestSchema,
  type StateCategory,
} from "../gen/kernel/v1/kernel_pb";
import { filterValue } from "./values";

export type FieldCondition = {
  field: string;
  op: "eq" | "gt" | "gte" | "lt" | "lte";
  // As typed; fieldType says how it is sent (values.filterValue).
  value: string;
  fieldType: FieldType;
};

export type SearchForm = {
  words: string;
  // Type IDs (ticket), without namespace or version.
  types: string[];
  states: string[];
  categories: StateCategory[];
  fields: FieldCondition[];
  // A field ID, or "" for newest first.
  orderBy: string;
  desc: boolean;
  // Items linked to (or from) one Item: the board, and "see all" links.
  linked?: { direction: LinkDirection; linkType: string; item: string };
};

export const emptySearch: SearchForm = {
  words: "",
  types: [],
  states: [],
  categories: [],
  fields: [],
  orderBy: "",
  desc: false,
};

function value(c: FieldCondition): MessageInitShape<typeof ValueSchema> {
  const v = filterValue(c.fieldType, c.value);
  return typeof v === "boolean"
    ? { kind: { case: "boolValue", value: v } }
    : { kind: { case: "stringValue", value: String(v) } };
}

export function toFilter(ns: string, form: SearchForm): MessageInitShape<typeof ItemFilterSchema> {
  // Conditions on one field are one FieldFilter: a range has two bounds.
  const fields = new Map<string, MessageInitShape<typeof FieldFilterSchema>>();
  for (const c of form.fields) {
    const filter = fields.get(c.field) ?? { field: c.field };
    filter[c.op] = value(c);
    fields.set(c.field, filter);
  }
  return {
    types: form.types.map((id) => `namespaces/${ns}/types/${id}`),
    states: form.states,
    categories: form.categories,
    fields: [...fields.values()],
    links: form.linked ? [form.linked] : [],
  };
}

// No filter at all is sent as none, not as an empty one.
function filterOrNone(ns: string, form: SearchForm) {
  const f = toFilter(ns, form);
  const empty = [f.types, f.states, f.categories, f.fields, f.links].every((list) => !list || list.length === 0);
  return empty ? undefined : f;
}

const order = (form: SearchForm) => (form.orderBy === "" ? "" : form.orderBy + (form.desc ? " desc" : ""));

// minSeq: the last commit_seq this tab received from a write, for
// read-your-writes; 0n when there is none.
export function toSearchRequest(
  ns: string,
  form: SearchForm,
  opts: { minSeq: bigint; pageToken: string; pageSize: number },
): MessageInitShape<typeof SearchItemsRequestSchema> {
  return {
    parent: `namespaces/${ns}`,
    query: form.words.trim(),
    minSeq: opts.minSeq,
    filter: filterOrNone(ns, form),
    pageSize: opts.pageSize,
    pageToken: opts.pageToken,
    orderBy: order(form),
  };
}

// parent: a container's name, or namespaces/{ns} for the top level. The
// form's words are not used: a list has none.
export function toListRequest(
  ns: string,
  parent: string,
  form: SearchForm,
  opts: { pageToken: string; pageSize: number },
): MessageInitShape<typeof ListItemsRequestSchema> {
  return {
    parent,
    filter: filterOrNone(ns, form),
    pageSize: opts.pageSize,
    pageToken: opts.pageToken,
    orderBy: order(form),
  };
}
