package testdb

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StateEnteredAtDrift lists the Items (at most 20) whose state_entered_at
// is not the time of the event that put them in their state: their latest
// create or transition event, or their create time when they have none
// (rows a test seeded by hand). It is one statement, so it reads one
// snapshot: an Item's row and its event are written together.
//
// The test packages that write Items call it after their whole run, over
// every Item every test made, by whatever operation (track 1, item 5, step
// 3a): a write path that forgets the column, or sets it when no state
// changed, shows here whatever test exercised it.
func StateEnteredAtDrift(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx,
		`select i.id::text || ' (' || i.type_id || '@' || i.type_version || ', in ' || i.state || '): state_entered_at ' ||
		        i.state_entered_at::text || ', entered ' || e.at::text
		 from items i
		 cross join lateral (
		     select coalesce((select created_at from item_events
		                      where item_id = i.id and op in ('create', 'transition')
		                      order by item_seq desc limit 1), i.created_at) as at
		 ) e
		 where i.state_entered_at is distinct from e.at
		 limit 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
