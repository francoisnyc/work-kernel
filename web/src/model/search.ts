// The search and list forms as requests. The UI builds the filter the user
// asked for and nothing more: whether a field can be filtered or sorted
// across the chosen Types is the server's answer.
//
// Phase B: signatures only.
import type { MessageInitShape } from "@bufbuild/protobuf";
import type {
  FieldType,
  ItemFilterSchema,
  LinkDirection,
  ListItemsRequestSchema,
  SearchItemsRequestSchema,
  StateCategory,
} from "../gen/kernel/v1/kernel_pb";

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

export function toFilter(_ns: string, _form: SearchForm): MessageInitShape<typeof ItemFilterSchema> {
  throw new Error("not implemented");
}

// minSeq: the last commit_seq this tab received from a write, for
// read-your-writes; 0n when there is none.
export function toSearchRequest(
  _ns: string,
  _form: SearchForm,
  _opts: { minSeq: bigint; pageToken: string; pageSize: number },
): MessageInitShape<typeof SearchItemsRequestSchema> {
  throw new Error("not implemented");
}

// parent: a container's name, or namespaces/{ns} for the top level. The
// form's words are not used: a list has none.
export function toListRequest(
  _ns: string,
  _parent: string,
  _form: SearchForm,
  _opts: { pageToken: string; pageSize: number },
): MessageInitShape<typeof ListItemsRequestSchema> {
  throw new Error("not implemented");
}
