// Package acts is the shared activity library: everything a workflow does
// in the outside world. Workflows must be deterministic, so I/O lives here.
package acts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"

	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/store"
)

// Activities holds what the activities need. Temporal registers its
// exported methods; a workflow names them through a nil *Activities.
type Activities struct {
	Store    *store.Store
	HRURL    string // the HR directory
	ERPURL   string // the purchase system
	EmailURL string
	HTTP     *http.Client
}

func New(s *store.Store, hr, erp, email string) *Activities {
	return &Activities{Store: s, HRURL: hr, ERPURL: erp, EmailURL: email, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Record writes a request's picture for the inbox, and an audit line.
func (a *Activities) Record(ctx context.Context, row store.Row, ev *store.Event) error {
	return a.Store.Record(ctx, row, ev)
}

// ErrUnknownEmployee is not retried: asking again will not find a manager.
const ErrUnknownEmployee = "UNKNOWN_EMPLOYEE"

// ManagerOf asks the HR directory who an employee's manager is.
func (a *Activities) ManagerOf(ctx context.Context, employee string) (string, error) {
	var out struct {
		Manager string `json:"manager"`
	}
	status, err := a.call(ctx, http.MethodGet, a.HRURL+"/employees/"+url.PathEscape(employee)+"/manager", "", nil, &out)
	if status == http.StatusNotFound {
		return "", temporal.NewNonRetryableApplicationError("no manager for "+employee, ErrUnknownEmployee, nil)
	}
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(out.Manager, "user:") {
		return "", temporal.NewNonRetryableApplicationError("the HR directory named no person", ErrUnknownEmployee, nil)
	}
	return out.Manager, nil
}

// PlaceOrder sends the order to the purchase system, once per key.
func (a *Activities) PlaceOrder(ctx context.Context, key, vendor string, amount int64) (string, error) {
	var out struct {
		ID string `json:"invoice_id"`
	}
	_, err := a.call(ctx, http.MethodPost, a.ERPURL+"/invoices", key, map[string]any{"customer_id": vendor, "amount": amount}, &out)
	return out.ID, err
}

// Notify tells a person about a request (the email stub).
func (a *Activities) Notify(ctx context.Context, key, to, template, request string) error {
	_, err := a.call(ctx, http.MethodPost, a.EmailURL+"/messages", key,
		map[string]any{"to": to, "template": template, "item": request, "key": key}, nil)
	return err
}

// call makes one JSON request; a status of 300 or more is an error.
func (a *Activities) call(ctx context.Context, method, target, key string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d", method, target, resp.StatusCode)
	}
	if out != nil {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}
