// Package workflow holds the Temporal side of an Item: a thin scheduler of
// timers and waits. It decides nothing about the lifecycle; when a timer
// fires it asks the kernel to make the move, through the normal write path.
//
// The package has two kinds of code, with very different rules:
//
//   - item.go is workflow code. Temporal re-runs ("replays") it from recorded
//     history, so it must be deterministic: no time.Now, randomness,
//     goroutines, map iteration or I/O. It uses the SDK's equivalents.
//   - activities.go is ordinary Go that does the I/O the workflow may not.
package workflow
