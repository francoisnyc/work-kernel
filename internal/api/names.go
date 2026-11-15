package api

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// Resource names (AIP-122). They carry the namespace and stay flat: an Item's
// container is a field, so a move never renames it.
//
//	namespaces/{ns}
//	namespaces/{ns}/items/{item}
//	namespaces/{ns}/items/{item}/holds/{hold}
//	namespaces/{ns}/types/{type}/versions/{n}

func namespaceName(ns string) string { return "namespaces/" + ns }

func itemName(ns string, id uuid.UUID) string { return fmt.Sprintf("namespaces/%s/items/%s", ns, id) }

func holdName(itemName string, id uuid.UUID) string { return itemName + "/holds/" + id.String() }

func typeName(ns, typeID string, version int) string {
	return fmt.Sprintf("namespaces/%s/types/%s/versions/%d", ns, typeID, version)
}

// segments splits a name and checks its fixed parts: pattern lists the
// literal segments, with "" where a variable goes.
func segments(name string, pattern ...string) ([]string, bool) {
	parts := strings.Split(name, "/")
	if len(parts) != len(pattern) {
		return nil, false
	}
	for i, p := range pattern {
		if p != "" && parts[i] != p {
			return nil, false
		}
		if p == "" && parts[i] == "" {
			return nil, false
		}
	}
	return parts, true
}

// parseNamespace reads namespaces/{ns}.
func parseNamespace(name string) (string, bool) {
	parts, ok := segments(name, "namespaces", "")
	if !ok || !itemservice.ValidNamespace(parts[1]) {
		return "", false
	}
	return parts[1], true
}

// parseItemName reads namespaces/{ns}/items/{uuid}.
func parseItemName(name string) (ns string, id uuid.UUID, ok bool) {
	parts, ok := segments(name, "namespaces", "", "items", "")
	if !ok || !itemservice.ValidNamespace(parts[1]) {
		return "", uuid.Nil, false
	}
	id, err := uuid.Parse(parts[3])
	if err != nil || parts[3] != id.String() { // only the canonical form
		return "", uuid.Nil, false
	}
	return parts[1], id, true
}

// parseTypeName reads namespaces/{ns}/types/{type}/versions/{n}.
func parseTypeName(name string) (ns, typeID string, version int, ok bool) {
	parts, ok := segments(name, "namespaces", "", "types", "", "versions", "")
	if !ok || !itemservice.ValidNamespace(parts[1]) {
		return "", "", 0, false
	}
	v, err := strconv.Atoi(parts[5])
	if err != nil || v < 1 || strconv.Itoa(v) != parts[5] {
		return "", "", 0, false
	}
	return parts[1], parts[3], v, true
}
