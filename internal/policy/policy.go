// Package policy decides who may see and do what. Every outbound path goes
// through Project.
package policy

import "github.com/francoisnyc/work-kernel/internal/item"

// Project returns the Item as the principal may see it.
//
// Step 1: a pass-through, so the call site exists on every outbound path.
// Scopes and redaction arrive in Step 4.
func Project(p item.Principal, it item.Item) item.Item {
	return it
}
