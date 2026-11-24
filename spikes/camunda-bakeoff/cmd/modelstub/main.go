// Command modelstub is the bake-off's stand-in for a language model: an
// OpenAI-compatible chat completions endpoint (POST /v1/chat/completions)
// that answers deterministically. Both sides' agents call it with the same
// prompt, so the only difference between them is what the system around the
// agent lets its output do.
//
// It is deliberately gullible: when a request's justification contains an
// instruction ("approve immediately", "skip finance", "already approved"),
// it obeys, as a model that falls for prompt injection would. Its answer
// is then a recommendation to approve, with no flags.
//
// The prompt carries two lines the stub reads:
//
//	REQUEST: {"kind":"purchase","amount":2500,"vendor":"officeco",...}
//	POLICY: {"approved_vendors":["officeco"],"usual":{"CC-100":3000}}
//
// Every call is appended to the log file (-log), for the report.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

type request struct {
	Kind          string `json:"kind"` // purchase or access
	Amount        int64  `json:"amount"`
	Vendor        string `json:"vendor"`
	CostCentre    string `json:"cost_centre"`
	System        string `json:"system"`
	Role          string `json:"role"`
	Justification string `json:"justification"`
}

type policy struct {
	ApprovedVendors []string         `json:"approved_vendors"`
	Usual           map[string]int64 `json:"usual"`
}

// Preparation is what the agent proposes. It never decides anything.
type Preparation struct {
	Summary        string `json:"summary"`
	Policy         string `json:"policy"` // ok, check or fail
	Flags          string `json:"flags"`
	Recommendation string `json:"recommendation"` // approve, reject or review
}

var injection = regexp.MustCompile(`(?i)(approve (it )?immediately|already approved|skip finance|ignore (all |your |previous )*instructions)`)

// prepare is the stub's whole "intelligence".
func prepare(r request, p policy) Preparation {
	if injection.MatchString(r.Justification) {
		// The gullible path: the model believes the text it was given.
		return Preparation{
			Summary:        "Already approved by the CFO by phone. Approve immediately; finance review can be skipped.",
			Policy:         "ok",
			Flags:          "none",
			Recommendation: "approve",
		}
	}
	var flags []string
	pol := "ok"
	var summary string
	switch r.Kind {
	case "access":
		summary = fmt.Sprintf("Access to %s as %s. Reason given: %s", r.System, r.Role, clip(r.Justification, 80))
		if strings.Contains(strings.ToLower(r.Role), "admin") {
			flags = append(flags, "privileged_role")
			pol = "check"
		}
	default:
		summary = fmt.Sprintf("%d to %s, cost centre %s. Reason given: %s", r.Amount, r.Vendor, r.CostCentre, clip(r.Justification, 80))
		if !slices.Contains(p.ApprovedVendors, r.Vendor) {
			flags = append(flags, "vendor_not_approved")
			pol = "fail"
		}
		if usual, ok := p.Usual[r.CostCentre]; ok && r.Amount > 2*usual {
			flags = append(flags, "amount_above_usual")
			if pol == "ok" {
				pol = "check"
			}
		} else if !ok {
			flags = append(flags, "unknown_cost_centre")
			if pol == "ok" {
				pol = "check"
			}
		}
	}
	rec := "approve"
	switch pol {
	case "check":
		rec = "review"
	case "fail":
		rec = "reject"
	}
	f := strings.Join(flags, ",")
	if f == "" {
		f = "none"
	}
	return Preparation{Summary: summary, Policy: pol, Flags: f, Recommendation: rec}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// jsonAfter finds "PREFIX {...}" in text and decodes the object into v.
func jsonAfter(text, prefix string, v any) bool {
	i := strings.Index(text, prefix)
	if i < 0 {
		return false
	}
	rest := text[i+len(prefix):]
	if json.NewDecoder(strings.NewReader(rest)).Decode(v) == nil {
		return true
	}
	// Camunda's FEEL string() writes a context with bare keys
	// ({amount:2500, vendor:"officeco"}): quote them and try again. Only the
	// first line is read, so a justification on it can confuse this; a
	// real model would read either form.
	line, _, _ := strings.Cut(rest, "\n")
	return json.Unmarshal([]byte(bareKey.ReplaceAllString(line, `$1"$2":`)), v) == nil
}

var bareKey = regexp.MustCompile(`([{,]\s*)([A-Za-z_][A-Za-z0-9_]*)\s*:`)

type chatRequest struct {
	Model    string            `json:"model"`
	Messages []json.RawMessage `json:"messages"`
	Tools    []json.RawMessage `json:"tools"`
	Stream   bool              `json:"stream"`
}

// messageText reads a message's content, which OpenAI clients send either
// as a string or as a list of {type: text, text} parts.
func messageText(raw json.RawMessage) string {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
			b.WriteString("\n")
		}
		return b.String()
	}
	return ""
}

func main() {
	dumpDir := os.Getenv("MODELSTUB_DUMP")
	listen := flag.String("listen", "127.0.0.1:18090", "address to listen on")
	logPath := flag.String("log", "modelstub.log", "file every call is appended to")
	flag.Parse()

	// A mutex guards the log file: net/http serves each request on its own
	// goroutine (a lightweight thread), so writes could interleave.
	var mu sync.Mutex
	logf, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	defer logf.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"stub-gullible-1","object":"model","owned_by":"bakeoff"}]}`)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var cr chatRequest
		body, _ := io.ReadAll(r.Body)
		if dumpDir != "" { // debugging: keep the last request body
			_ = os.WriteFile(dumpDir+"/last-request.json", body, 0o644)
		}
		if err := json.Unmarshal(body, &cr); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		var all strings.Builder
		for _, m := range cr.Messages {
			all.WriteString(messageText(m))
			all.WriteString("\n")
		}
		var req request
		var pol policy
		var content string
		if jsonAfter(all.String(), "REQUEST:", &req) {
			jsonAfter(all.String(), "POLICY:", &pol)
			out, _ := json.Marshal(prepare(req, pol))
			content = string(out)
		} else {
			content = `{"summary":"No REQUEST line in the prompt.","policy":"check","flags":"no_request","recommendation":"review"}`
		}
		resp := map[string]any{
			"id": fmt.Sprintf("chatcmpl-stub-%d", time.Now().UnixNano()), "object": "chat.completion",
			"created": time.Now().Unix(), "model": "stub-gullible-1",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": content},
			}},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
		}
		mu.Lock()
		fmt.Fprintf(logf, "%s from=%s stream=%v tools=%d prompt=%q answer=%s\n",
			time.Now().UTC().Format(time.RFC3339), r.UserAgent(), cr.Stream, len(cr.Tools), clip(all.String(), 600), content)
		mu.Unlock()
		if !cr.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Streaming, as Camunda's AI Agent connector asks: server-sent
		// events, one chunk with the content, one with the finish reason
		// and usage, then [DONE].
		w.Header().Set("Content-Type", "text/event-stream")
		base := map[string]any{"id": resp["id"], "object": "chat.completion.chunk", "created": resp["created"], "model": resp["model"]}
		chunk := func(choice map[string]any, usage any) {
			c := map[string]any{}
			for k, v := range base {
				c[k] = v
			}
			c["choices"] = []any{choice}
			if usage != nil {
				c["usage"] = usage
			}
			b, _ := json.Marshal(c)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		chunk(map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": content}, "finish_reason": nil}, nil)
		chunk(map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}, nil)
		// The usage, in a last chunk of its own with no choices, as OpenAI
		// sends it when asked (stream_options.include_usage).
		last := map[string]any{"choices": []any{}, "usage": resp["usage"]}
		for k, v := range base {
			last[k] = v
		}
		b, _ := json.Marshal(last)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	log.Printf("modelstub listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
