package main

// Variant B, on the throwaway branch spike/variant-b only (never merged):
// the ERP check as a synchronous call before the write transaction, with a
// 2 s timeout, on ticket_a (no prefetch, no guard). It has no hold and no
// wait: the call answers applied, denied, or "unavailable" (the ERP did not
// answer in 2 s, or failed), which counts as not answered.

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func init() {
	extraVariants = append(extraVariants, func(b *variantBench) variant {
		kB := itemservice.NewWithOptions(b.pool, itemservice.Options{
			CacheTypes: true, SyncChecks: hooks.NewConnectors(b.crm.URL, b.erp.URL), SyncCheckTimeout: 2 * time.Second,
		})
		once := func(ctx context.Context, b *variantBench, id uuid.UUID) (string, error) {
			res, err := kB.Transition(ctx, itemservice.TransitionRequest{
				IdempotencyKey: key(), Principal: b.ana, ItemID: id, ToState: "approved",
			})
			switch {
			case errors.Is(err, itemservice.ErrUnavailable):
				return "unavailable", nil
			case errors.Is(err, itemservice.ErrRejected):
				return "denied_check", nil
			case err != nil:
				return "", err
			case res.Outcome != itemservice.OutcomeApplied:
				return string(res.Outcome), nil
			}
			return "applied", nil
		}
		return variant{
			name: "B", typeID: "ticket_a", version: 1,
			decide: func(ctx context.Context, b *variantBench, id uuid.UUID, _ time.Duration) (string, time.Duration, error) {
				start := time.Now()
				o, err := once(ctx, b, id)
				return o, time.Since(start), err
			},
			request: func(ctx context.Context, b *variantBench, id uuid.UUID) error {
				_, err := once(ctx, b, id)
				return err
			},
		}
	})
}
