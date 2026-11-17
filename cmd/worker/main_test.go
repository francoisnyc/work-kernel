// Tests for proof-of-concept Step 2, part 3: the worker's dev clock. A day
// of the durations Types declare can be made short for the scenario, never
// by default and never for every Type: --dev-time-scale is refused unless
// --dev-clock-i-know-this-is-not-production and --dev-time-scale-types (the
// Types it applies to, each one loaded) are given too, and every start on a
// scaled clock warns loudly, naming the Types.
package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

const scale, ack, scaled = "--" + flagTimeScale, "--" + flagDevClock, "--" + flagTimeScaleTypes

func TestParseConfig_RealTimeByDefault(t *testing.T) {
	c, err := parseConfig(nil)
	if err != nil || c.DevDay != 0 || len(c.DevTypes) != 0 {
		t.Errorf("no flags = %+v, %v; want real time", c, err)
	}
	if w := clockWarning(c); w != "" {
		t.Errorf("warning on real time = %q, want none", w)
	}
	// The acknowledgment alone changes nothing.
	if c, err := parseConfig([]string{ack}); err != nil || c.DevDay != 0 {
		t.Errorf("the dev flag alone = %+v, %v; want real time", c, err)
	}
}

func TestParseConfig_ScaleNeedsTheDevFlagAndTheTypes(t *testing.T) {
	refused := [][]string{
		{scale, "2s", scaled, "vacation_request"},       // no acknowledgment
		{scale, "2s", ack},                              // no Types: never "all Types"
		{scale, "2s", ack, scaled, ""},                  // an empty list
		{scale, "2s", ack, scaled, "vacation_request,"}, // an empty name in it
		{ack, scaled, "vacation_request"},               // Types without a scale
	}
	for _, args := range refused {
		if c, err := parseConfig(args); err == nil {
			t.Errorf("%v accepted (%+v), want refused", args, c)
		}
	}
	c, err := parseConfig([]string{scale, "2s", ack, scaled, "vacation_request,ticket"})
	if err != nil || c.DevDay != 2*time.Second || !slices.Equal(c.DevTypes, []string{"vacation_request", "ticket"}) {
		t.Fatalf("all three = %+v, %v; want a day of 2s for vacation_request and ticket", c, err)
	}
	w := clockWarning(c)
	for _, want := range []string{"NOT", "production", "2s", "vacation_request", "ticket"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q does not say %q", w, want)
		}
	}
}

func TestParseConfig_BadScalesRefused(t *testing.T) {
	for _, v := range []string{"0s", "-1s", "two", "48h"} {
		if _, err := parseConfig([]string{scale, v, ack, scaled, "vacation_request"}); err == nil {
			t.Errorf("--dev-time-scale %s accepted; want a positive duration shorter than a day", v)
		}
	}
}

// At start, each named Type must be one the item service has loaded.
func TestCheckDevTypes(t *testing.T) {
	loaded := []string{"channel", "ticket", "vacation_request"}
	if err := checkDevTypes(config{DevDay: time.Second, DevTypes: []string{"vacation_request"}}, loaded); err != nil {
		t.Errorf("a loaded Type: %v", err)
	}
	err := checkDevTypes(config{DevDay: time.Second, DevTypes: []string{"vacation_request", "vacation"}}, loaded)
	if err == nil || !strings.Contains(err.Error(), "vacation") {
		t.Errorf("an unknown Type: %v, want refused, naming it", err)
	}
	if err := checkDevTypes(config{}, loaded); err != nil {
		t.Errorf("real time: %v, want nothing to check", err)
	}
}
