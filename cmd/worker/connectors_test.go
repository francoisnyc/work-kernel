// Tests for the hook protocol spike: --connectors names the connectors
// file (deploy/connectors.yaml in the compose stack), which says which
// connector@major is a remote service. It goes with or without the dev
// clock; none means every connector is compiled in (and hr@1, which is
// remote only, is unconfigured).
package main

import "testing"

const connectorsFlag = "--" + flagConnectors

func TestParseConfig_Connectors(t *testing.T) {
	c, err := parseConfig([]string{connectorsFlag, "/etc/worker/connectors.yaml"})
	if err != nil || c.Connectors != "/etc/worker/connectors.yaml" || c.DevDay != 0 {
		t.Errorf("--connectors alone = %+v, %v; want the path, real time", c, err)
	}
	c, err = parseConfig([]string{connectorsFlag, "/etc/worker/connectors.yaml", scale, "2s", ack, scaled, "vacation_request"})
	if err != nil || c.Connectors != "/etc/worker/connectors.yaml" || c.DevDay == 0 {
		t.Errorf("--connectors with the dev clock = %+v, %v; want both", c, err)
	}
	if c, err := parseConfig(nil); err != nil || c.Connectors != "" {
		t.Errorf("no flags = %+v, %v; want no connectors file", c, err)
	}
	if _, err := parseConfig([]string{connectorsFlag, ""}); err == nil {
		t.Error("--connectors with an empty path was accepted")
	}
}
