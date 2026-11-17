// Tests for proof-of-concept Step 2, part 3: the worker's dev clock. A day
// of the durations Types declare can be made short for the scenario, never
// by default: --dev-time-scale is refused unless
// --dev-clock-i-know-this-is-not-production is given too, and every start
// on a scaled clock warns loudly.
package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseConfig_RealTimeByDefault(t *testing.T) {
	c, err := parseConfig(nil)
	if err != nil || c.DevDay != 0 {
		t.Errorf("no flags = %+v, %v; want real time", c, err)
	}
	if w := clockWarning(c); w != "" {
		t.Errorf("warning on real time = %q, want none", w)
	}
}

func TestParseConfig_ScaleNeedsTheDevFlag(t *testing.T) {
	if _, err := parseConfig([]string{"--" + flagTimeScale, "2s"}); err == nil {
		t.Error("--dev-time-scale alone was accepted; it needs --" + flagDevClock)
	}
	c, err := parseConfig([]string{"--" + flagTimeScale, "2s", "--" + flagDevClock})
	if err != nil || c.DevDay != 2*time.Second {
		t.Fatalf("both flags = %+v, %v; want a day of 2s", c, err)
	}
	w := clockWarning(c)
	for _, want := range []string{"NOT", "production", "2s"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q does not say %q", w, want)
		}
	}
	// The acknowledgment alone changes nothing.
	if c, err := parseConfig([]string{"--" + flagDevClock}); err != nil || c.DevDay != 0 {
		t.Errorf("the dev flag alone = %+v, %v; want real time", c, err)
	}
}

func TestParseConfig_BadScalesRefused(t *testing.T) {
	for _, v := range []string{"0s", "-1s", "two", "48h"} {
		if _, err := parseConfig([]string{"--" + flagTimeScale, v, "--" + flagDevClock}); err == nil {
			t.Errorf("--dev-time-scale %s accepted; want a positive duration shorter than a day", v)
		}
	}
}
