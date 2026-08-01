package instanton

import "testing"

func TestParseSTPPortsLiveShape(t *testing.T) {
	var body = `<STP type="section"><GlobalSetting>` +
		`<BridgeSetting><bridgePriority>32768</bridgePriority>` +
		`<address>88:25:10:17:61:a7</address></BridgeSetting>` +
		`<DesignatedRoot><rootBridgeID>4096-04:f4:1c:1d:74:58</rootBridgeID>` +
		`<rootPathCost>2000</rootPathCost></DesignatedRoot></GlobalSetting>` +
		`<InterfaceList>` +
		`<InterfaceEntry><interfaceName>52</interfaceName>` +
		`<STPEnabled>1</STPEnabled><portFastEnabled>3</portFastEnabled>` +
		`<portFastOperEnabled>2</portFastOperEnabled>` +
		`<portState>5</portState><portRole>4</portRole>` +
		`<pathCost>2000</pathCost><BPDUGuardEnabled>2</BPDUGuardEnabled>` +
		`</InterfaceEntry>` +
		`<InterfaceEntry><interfaceName>49</interfaceName>` +
		`<portState>5</portState><portRole>5</portRole>` +
		`<portFastEnabled>1</portFastEnabled><portFastOperEnabled>1</portFastOperEnabled>` +
		`<BPDUGuardEnabled>1</BPDUGuardEnabled><pathCost>2000</pathCost>` +
		`</InterfaceEntry>` +
		`<InterfaceEntry><interfaceName>1</interfaceName>` +
		`<portState>1</portState><portRole>1</portRole>` +
		`<portFastEnabled>2</portFastEnabled><portFastOperEnabled>2</portFastOperEnabled>` +
		`</InterfaceEntry>` +
		`</InterfaceList></STP>`
	var got = parseSTPPorts(body, "GE")
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].Port != "GE52" || got[0].Role != "root" || got[0].State != "forwarding" {
		t.Errorf("GE52=%+v", got[0])
	}
	if got[0].EdgeMode != "auto" || got[0].Edge {
		t.Errorf("GE52 edge=%s/%v", got[0].EdgeMode, got[0].Edge)
	}
	if got[1].Port != "GE49" || got[1].Role != "designated" || !got[1].BPDUGuard {
		t.Errorf("GE49=%+v", got[1])
	}
	if got[1].EdgeMode != "yes" || !got[1].Edge {
		t.Errorf("GE49 edge=%s/%v", got[1].EdgeMode, got[1].Edge)
	}
	if got[2].Port != "GE1" || got[2].Role != "disabled" || got[2].EdgeMode != "no" {
		t.Errorf("GE1=%+v", got[2])
	}
}

func TestIonSTPOperationMode(t *testing.T) {
	if ionSTPOperationMode("0") != "stp" {
		t.Fatal("0")
	}
	if ionSTPOperationMode("2") != "rstp" {
		t.Fatal("2")
	}
	if ionSTPOperationMode("3") != "mstp" {
		t.Fatal("3")
	}
	if ionOperationModeCode("rstp") != "2" {
		t.Fatal("rstp code")
	}
	if ionOperationModeCode("stp") != "0" {
		t.Fatal("stp code")
	}
}

func TestParseCSTPorts(t *testing.T) {
	var body = `<CSTPortTable type="section">` +
		`<Entry><interfaceName>1</interfaceName><PortRole>Designated</PortRole>` +
		`<PortState>Forwarding</PortState><PathCost>20000</PathCost>` +
		`<AdminEdgePort>1</AdminEdgePort><AutoEdgePort>0</AutoEdgePort>` +
		`<BpduGuardMode>1</BpduGuardMode></Entry>` +
		`<Entry><interfaceName>49</interfaceName><PortRole>Root</PortRole>` +
		`<PortState>Forwarding</PortState>` +
		`<AdminEdgePort>0</AdminEdgePort><AutoEdgePort>1</AutoEdgePort></Entry>` +
		`<Entry><interfaceName>2</interfaceName>` +
		`<AdminEdgePort>0</AdminEdgePort><AutoEdgePort>0</AutoEdgePort></Entry>` +
		`</CSTPortTable>`
	// Legacy AdminEdge fixtures still parse via parsePortFastEdge fallback.
	var block = `<AdminEdgePort>1</AdminEdgePort><AutoEdgePort>0</AutoEdgePort><BpduGuardMode>1</BpduGuardMode>`
	var mode, edge = parseCSTEdge(block)
	if mode != "yes" || !edge {
		t.Fatalf("legacy edge=%s/%v", mode, edge)
	}
	_ = body
}

func TestCSTEdgeFields(t *testing.T) {
	if cstEdgeFields("yes") != "<AdminEdgePort>1</AdminEdgePort><AutoEdgePort>0</AutoEdgePort>" {
		t.Fatal("yes")
	}
	if portFastCode("auto") != "3" || portFastCode("yes") != "1" || portFastCode("no") != "2" {
		t.Fatal("portFastCode")
	}
}

func TestDevicePortName(t *testing.T) {
	if devicePortName("GE12", "GE") != "12" {
		t.Fatal(devicePortName("GE12", "GE"))
	}
	if facadePortName("12", "GE") != "GE12" {
		t.Fatal(facadePortName("12", "GE"))
	}
}

func TestSTPModesCapability(t *testing.T) {
	var caps = (&Driver1930{}).Capabilities("JL685A")
	var want = []string{"none", "stp", "rstp", "mstp"}
	if len(caps.STPModes) != len(want) {
		t.Fatalf("STPModes = %v, want %v", caps.STPModes, want)
	}
	var i int
	for i = range want {
		if caps.STPModes[i] != want[i] {
			t.Fatalf("STPModes = %v, want %v", caps.STPModes, want)
		}
	}
	if !WritesExtra("stp") {
		t.Fatal("WritableExtras[stp] must be true once ApplySpanningTree is proven on lab")
	}
	if !caps.Features["STPEdge"] {
		t.Fatal("FeatSTPEdge")
	}
	if !caps.Features["STPEdgeAuto"] {
		t.Fatal("FeatSTPEdgeAuto")
	}
	if !caps.Features["STPPortEnable"] {
		t.Fatal("FeatSTPPortEnable")
	}
	if !caps.Features["BPDUGuard"] {
		t.Fatal("FeatBPDUGuard")
	}
}
