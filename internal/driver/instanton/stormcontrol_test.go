package instanton

import (
	"context"
	"errors"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// "storm_control" must stay out of WritableExtras, and unlike every other
// connector in the fleet that is NOT a staging state waiting on a capture: there
// is nothing per-port on this hardware to declare, and there never will be.
func TestWritesExtraStormControlStaysFalse(t *testing.T) {
	if WritesExtra("storm_control") {
		t.Fatal("storm_control is declared writable on a range whose storm control is a " +
			"single device-wide setting")
	}
	if (&Driver1930{}).WritesExtra("storm_control") {
		t.Fatal("the connector announces storm_control")
	}
	// The two that ARE declared must survive: an over-eager edit to WritableExtras
	// would take working buttons with it.
	if !WritesExtra("stp") || !WritesExtra("port_admin") {
		t.Fatal("the capabilities this connector does declare were lost")
	}
}

// AN EMPTY CLASS LIST is the answer that lets a screen say "this switch cannot"
// rather than "meshcraft has not proven it yet". Only one of those is true here,
// and no capture will change it.
func TestStormControlCapsExpressNothingPerPort(t *testing.T) {
	var caps = (&Driver1930{}).StormControlCaps()
	var class driver.StormClass
	for _, class = range driver.StormClasses {
		if caps.Expresses(class) {
			t.Errorf("%s is declared on a range with no per-port storm control", class)
		}
	}
	if len(caps.Units) != 0 {
		t.Error("a rate unit is declared for a write that cannot happen")
	}
	if len(caps.Why) == 0 {
		t.Fatal("an empty capability with no reason leaves the operator nothing to read")
	}
	// The reason must name the SCOPE, because the operator's next question is
	// "can I set it anywhere at all?" — and the answer is yes, device-wide.
	var joined = strings.Join(caps.Why, " ")
	if !strings.Contains(joined, "DEVICE-WIDE") {
		t.Error("the refusal does not say that a device-wide setting exists")
	}
}

// The refusal must be a CAPABILITY answer — errors.Is(err, ErrNotImplemented) —
// and must not reach the device. A transport error would tell an operator to
// check their network for a fact about their switch's feature set.
func TestApplyStormControlRefusesWithoutReachingTheDevice(t *testing.T) {
	// An Admin with no HTTP client behind it: any attempt to reach the device
	// would panic or error on transport, so a clean capability refusal here is
	// itself the proof that nothing was sent.
	var a = &Admin{}
	var plan = driver.StormControlPlan{Ports: []driver.StormControlPortIntent{{
		Port: "GE1",
		Limits: []driver.StormLimit{
			{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
		},
	}}}
	var res, err = a.ApplyStormControl(context.Background(), plan)
	if err == nil {
		t.Fatal("a per-port storm control write was accepted on a range that has none")
	}
	if !errors.Is(err, driver.ErrNotImplemented) {
		t.Errorf("the refusal is not a capability answer: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Error("a refusal reported ports as written")
	}
	// THE SUBSTITUTION THAT WAS REFUSED, named in the message: writing the global
	// setting would limit every jack on the box at a threshold chosen for one.
	if !strings.Contains(err.Error(), "EVERY jack") {
		t.Errorf("the refusal does not say what writing the global setting would have "+
			"cost: %v", err)
	}
}

// The READER is deliberately absent. A reader returning an empty state would be
// indistinguishable from "no limiters configured", and would put "no storm
// control on this port" on screen for a switch that may be policing all of them
// device-wide.
func TestNoStormControlReaderIsImplemented(t *testing.T) {
	var adm driver.Admin = &Admin{}
	var _, ok = driver.AsStormControlReader(adm)
	if ok {
		t.Fatal("a per-port storm control reader exists on a range with nothing per-port " +
			"to read: an empty state would read on screen as \"no limiters\", which is a " +
			"claim about a device nobody questioned")
	}
}
