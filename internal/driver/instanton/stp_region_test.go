package instanton

import (
	"testing"

	"meshcraft/internal/driver"
)

// Fixture captured from hardware on 2026-08-02: 1930 JL685A,
// InstantOn_1930_3.4.0.0 (6), read through the proxy with the `MSTP` wcd section.
// The device was in MSTP mode with its region left at the platform default.
const mstpSectionLive = `<?xml version="1.0" encoding="UTF-8" ?>
<ResponseData>
<DeviceConfiguration>
<version>1.0</version>
<MSTP type="section">
<regionName>02:00:5e:10:61:a7</regionName>
<revisionNo>0</revisionNo>
<maxHops>20</maxHops>
<ISTMasterString>32768-02:00:5e:10:61:a7</ISTMasterString>
<InstanceList>
<InstanceEntry>
</InstanceEntry>
</InstanceList>
</MSTP>
</DeviceConfiguration>
<ActionStatus>
<requestURL>MSTP</requestURL>
<statusCode></statusCode>
</ActionStatus>
</ResponseData>`

func TestParseIonRegionFromLiveSection(t *testing.T) {
	var got = parseIonRegion(mstpSectionLive)
	if got.Name != "02:00:5e:10:61:a7" {
		t.Errorf("region name = %q, want the value the device serves", got.Name)
	}
	if got.Revision != 0 {
		t.Errorf("revision = %d, want 0", got.Revision)
	}
	// The instance map is deliberately NOT invented from an empty element.
	if len(got.InstanceVLANs) != 0 {
		t.Errorf("instance map = %v, want empty: the entry shape has never been observed",
			got.InstanceVLANs)
	}
}

// THE TRAP THIS EXISTS TO CATCH. The platform default region name is the bridge
// MAC, so a switch in MSTP mode with an untouched region is in a region of ONE —
// and reads as configured on every screen.
func TestIonRegionIsDefaultRecognisesTheBridgeMAC(t *testing.T) {
	var region = parseIonRegion(mstpSectionLive)
	if !IonRegionIsDefault(region, "02:00:5e:10:61:a7") {
		t.Error("a region named after the bridge MAC was not recognised as the default")
	}
	// Case and separators differ between the fields this UI serves.
	if !IonRegionIsDefault(region, "02005e1061A7") {
		t.Error("MAC comparison must survive formatting differences")
	}
	if IonRegionIsDefault(driver.MSTRegion{Name: "caserne-lab"}, "02:00:5e:10:61:a7") {
		t.Error("a real region name was reported as the default")
	}
	// An empty name is the default too: the device fills it in itself.
	if !IonRegionIsDefault(driver.MSTRegion{}, "02:00:5e:10:61:a7") {
		t.Error("an empty region name must count as default")
	}
}
