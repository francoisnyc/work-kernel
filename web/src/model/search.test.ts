import { create, toJson, type JsonObject } from "@bufbuild/protobuf";
import { expect, test } from "vitest";
import {
  FieldType,
  LinkDirection,
  ListItemsRequestSchema,
  SearchItemsRequestSchema,
  StateCategory,
} from "../gen/kernel/v1/kernel_pb";
import { itemName } from "../test/fixtures";
import { emptySearch, toListRequest, toSearchRequest, type SearchForm } from "./search";

// The request as the JSON that goes on the wire.
const searchJson = (form: SearchForm, opts = { minSeq: 0n, pageToken: "", pageSize: 50 }) =>
  toJson(SearchItemsRequestSchema, create(SearchItemsRequestSchema, toSearchRequest("acme", form, opts))) as JsonObject;

test("words alone", () => {
  expect(searchJson({ ...emptySearch, words: "printer" })).toEqual({
    parent: "namespaces/acme",
    query: "printer",
    pageSize: 50,
  });
});

test("Types, states and categories", () => {
  const form = {
    ...emptySearch,
    types: ["story", "bug"],
    states: ["todo"],
    categories: [StateCategory.TODO, StateCategory.ACTIVE],
  };
  expect(searchJson(form).filter).toEqual({
    types: ["namespaces/acme/types/story", "namespaces/acme/types/bug"],
    states: ["todo"],
    categories: ["STATE_CATEGORY_TODO", "STATE_CATEGORY_ACTIVE"],
  });
});

test("field conditions: eq, ranges, me, a bool", () => {
  const form: SearchForm = {
    ...emptySearch,
    fields: [
      { field: "f_assignee", op: "eq", value: "me", fieldType: FieldType.PRINCIPAL },
      { field: "f_points", op: "gte", value: "3", fieldType: FieldType.INT },
      { field: "f_end", op: "lt", value: "2026-12-31", fieldType: FieldType.DATE },
      { field: "f_urgent", op: "eq", value: "true", fieldType: FieldType.BOOL },
    ],
  };
  expect(searchJson(form).filter).toEqual({
    fields: [
      { field: "f_assignee", eq: "me" },
      { field: "f_points", gte: "3" },
      { field: "f_end", lt: "2026-12-31" },
      { field: "f_urgent", eq: true },
    ],
  });
});

test("two conditions on one field are one range", () => {
  const form: SearchForm = {
    ...emptySearch,
    fields: [
      { field: "f_end", op: "gte", value: "2026-12-01", fieldType: FieldType.DATE },
      { field: "f_end", op: "lte", value: "2026-12-31", fieldType: FieldType.DATE },
    ],
  };
  expect(searchJson(form).filter).toEqual({ fields: [{ field: "f_end", gte: "2026-12-01", lte: "2026-12-31" }] });
});

test("order_by: a field, ascending by default, desc when asked", () => {
  expect(searchJson({ ...emptySearch, types: ["story"], orderBy: "f_rank" }).orderBy).toBe("f_rank");
  expect(searchJson({ ...emptySearch, types: ["story"], orderBy: "f_points", desc: true }).orderBy).toBe("f_points desc");
  expect(searchJson({ ...emptySearch, words: "x", desc: true }).orderBy).toBeUndefined();
});

test("the board: Items linked to a sprint, by rank", () => {
  const form: SearchForm = {
    ...emptySearch,
    orderBy: "f_rank",
    linked: { direction: LinkDirection.OUTGOING, linkType: "sprint", item: itemName(9) },
  };
  expect(searchJson(form, { minSeq: 0n, pageToken: "", pageSize: 100 })).toEqual({
    parent: "namespaces/acme",
    filter: { links: [{ linkType: "sprint", direction: "LINK_DIRECTION_OUTGOING", item: itemName(9) }] },
    orderBy: "f_rank",
    pageSize: 100,
  });
});

test("read-your-writes and paging", () => {
  const json = searchJson({ ...emptySearch, words: "x" }, { minSeq: 42n, pageToken: "tok", pageSize: 20 });
  expect(json).toMatchObject({ minSeq: "42", pageToken: "tok", pageSize: 20 });
});

test("a list: a container's children with the same filter, no words", () => {
  const form = { ...emptySearch, words: "ignored", types: ["comment"], orderBy: "f_rank" };
  const req = toListRequest("acme", itemName(1), form, { pageToken: "", pageSize: 50 });
  expect(toJson(ListItemsRequestSchema, create(ListItemsRequestSchema, req))).toEqual({
    parent: itemName(1),
    filter: { types: ["namespaces/acme/types/comment"] },
    orderBy: "f_rank",
    pageSize: 50,
  });
});

test("the top level with no filter sends none", () => {
  const req = toListRequest("acme", "namespaces/acme", emptySearch, { pageToken: "", pageSize: 50 });
  expect(toJson(ListItemsRequestSchema, create(ListItemsRequestSchema, req))).toEqual({
    parent: "namespaces/acme",
    pageSize: 50,
  });
});
