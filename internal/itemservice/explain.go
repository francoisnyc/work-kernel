package itemservice

import (
	"context"
	"errors"
)

// explainSearch and explainList return the plan of the page query that
// Search or List would run, for the plan test of Jira part 2, step 4a
// (customer_plan_test.go, through export_test.go). Phase B stub: not built.
func (k *Service) explainSearch(ctx context.Context, req SearchRequest) (string, error) {
	return "", errors.New("explain: not built yet")
}

func (k *Service) explainList(ctx context.Context, req ListRequest) (string, error) {
	return "", errors.New("explain: not built yet")
}
