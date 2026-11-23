// Package contracts embeds the action contracts (Ventoux V1): one JSON file
// per operation, <connector>.<operation>@<major>.json, and contracts.lock,
// the digest of each published contract. Package internal/contracts reads
// them; the item service and the worker both compile them in, so a Type is
// checked against exactly the contracts the worker runs.
package contracts

import "embed"

// FS holds the contract files, compiled into the binary by go:embed.
//
//go:embed *.json contracts.lock
var FS embed.FS
