// Create an Item of any Type: the form is the Type's fields (GetType),
// optionally inside a container and narrowed to some readers.
import { useEffect, useState, type ReactElement } from "react";
import { go } from "../app/router";
import { useSession } from "../app/session";
import type { Type } from "../gen/kernel/v1/kernel_pb";
import { creatableByCustomer, isCustomer } from "../model/customers";
import { itemId, latestVersions } from "../model/typeinfo";
import { ItemForm } from "../ui/ItemForm";
import { ItemPicker } from "../ui/ItemPicker";

export function NewItemPage({
  container: initialContainer,
}: {
  container: string;
}): ReactElement {
  const session = useSession();
  const customer = isCustomer(session.user);
  const [typeId, setTypeId] = useState("");
  const [version, setVersion] = useState(0);
  const [container, setContainer] = useState(initialContainer);
  const [readers, setReaders] = useState("");
  // A customer is offered only what the container admits (Jira part 2,
  // step 4a): read from the container's Type, never decided here.
  const [containerType, setContainerType] = useState<Type>();
  useEffect(() => {
    setContainerType(undefined);
    if (!customer || container === "") return;
    let current = true;
    session.api.getItem({ name: session.itemName(container) }).then(
      (res) => current && setContainerType(session.typeOf(res.item!.type)),
      () => undefined,
    );
    return () => {
      current = false;
    };
  }, [customer, container, session]);
  const latest = customer
    ? creatableByCustomer(session.types, containerType)
    : latestVersions(session.types);

  const versions = session.types
    .filter((t) => t.typeId === typeId)
    .sort((a, b) => b.version - a.version);
  // The latest version unless another was chosen.
  const type = versions.find((t) => t.version === version) ?? versions[0];

  return (
    <>
      <h1>New Item</h1>
      <div className="item-form">
        <div className="field">
          <label htmlFor="new-type">Type</label>
          <select
            id="new-type"
            value={typeId}
            onChange={(e) => {
              setTypeId(e.target.value);
              setVersion(0);
            }}
          >
            <option value="">(choose a Type)</option>
            {latest.map((t) => (
              <option key={t.typeId} value={t.typeId}>
                {t.typeId}
              </option>
            ))}
          </select>
        </div>
        {type && (
          <div className="field">
            <label htmlFor="new-version">Version</label>
            <select
              id="new-version"
              value={type.version}
              onChange={(e) => setVersion(Number(e.target.value))}
            >
              {versions.map((t) => (
                <option key={t.version} value={t.version}>
                  {t.version}
                  {t === versions[0] ? " (latest)" : ""}
                </option>
              ))}
            </select>
          </div>
        )}
        <div className="field">
          <label htmlFor="new-container">Container (optional)</label>
          <ItemPicker
            id="new-container"
            value={container}
            onChange={setContainer}
            search={session.search}
            titleOf={session.titleOf}
          />
          <span className="hint">
            the Item this one is created inside; empty for the top level
          </span>
        </div>
        {!customer && (
          <div className="field">
            <label htmlFor="new-readers">Readers (optional)</label>
            <input
              id="new-readers"
              type="text"
              placeholder={`${session.user.principal}, user:…`}
              value={readers}
              onChange={(e) => setReaders(e.target.value)}
            />
            <span className="hint">
              narrow the Item to these principals, comma-separated; include
              yourself
            </span>
          </div>
        )}
      </div>
      {type && (
        <>
          <h2>
            Fields of {type.typeId}@{type.version}
          </h2>
          <ItemForm
            key={type.name}
            type={type}
            principals={session.principals}
            submitLabel="Create"
            onSubmit={async (fields) => {
              const principals = readers
                .split(",")
                .map((r) => r.trim())
                .filter((r) => r !== "");
              const res = await session.api.createItem({
                parent: session.parent,
                requestId: crypto.randomUUID(),
                type: type.name,
                container: session.itemName(container),
                fields,
                narrow: principals.length > 0 ? { principals } : undefined,
              });
              session.wrote(res.commitSeq);
              go(`#/items/${itemId(res.item!.name)}`);
            }}
          />
        </>
      )}
    </>
  );
}
