package instanton

// The MSTPInstanceList fixture below is the REAL response of sw-labo-1930
// (JL685A, InstantOn_1930_3.4.0.0), captured live on 2026-08-05 through the
// controller's raw wcd read — not a hand-written approximation. The parser is
// tested against what the device actually says, because this platform has no
// documentation to be right against.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

const mstInstanceListLive = `<?xml version="1.0" encoding="UTF-8" ?>
<ResponseData>
<DeviceConfiguration>
<version>1.0</version>
<MSTPInstanceList type="section">
<Entry>
<instanceID>0</instanceID>
<VLANList>2-41,43-49,51-97,101-4094</VLANList>
<priority>0</priority>
<enbl>0</enbl>
<topologyChgAge>0</topologyChgAge>
<topologyChgNo>0</topologyChgNo>
<brdgAddr>02:00:5e:10:61:a7</brdgAddr>
<dsgRootAddr>02:00:5e:10:61:a7</dsgRootAddr>
<dsgRootPriority>32768</dsgRootPriority>
<rootCost>0</rootCost>
<rootIfName>0</rootIfName>
<maxAge>0</maxAge>
<helloTime>0</helloTime>
<holdTime>0</holdTime>
<fwdDelay>0</fwdDelay>
<rmngHops>0</rmngHops>
</Entry>
<Entry>
<instanceID>1</instanceID>
<VLANList>1,42,50,98-100</VLANList>
<priority>32768</priority>
<enbl>1</enbl>
<topologyChgAge>27309</topologyChgAge>
<topologyChgNo>89</topologyChgNo>
<brdgAddr>02:00:5e:10:61:a7</brdgAddr>
<dsgRootAddr>02:00:5e:10:61:a7</dsgRootAddr>
<dsgRootPriority>32768</dsgRootPriority>
<rootCost>0</rootCost>
<rootIfName>0</rootIfName>
<maxAge>2000</maxAge>
<helloTime>200</helloTime>
<holdTime>100</holdTime>
<fwdDelay>1500</fwdDelay>
<rmngHops>20</rmngHops>
</Entry>
</MSTPInstanceList>
</DeviceConfiguration>
<ActionStatus>
<requestURL>MSTPInstanceList</requestURL>
<statusCode></statusCode>
</ActionStatus>
</ResponseData>
`

// mstInstanceListXML builds an MSTPInstanceList response for the fake device.
func mstInstanceListXML(entries ...[2]string) string {
	var out = `<?xml version="1.0" encoding="UTF-8" ?><ResponseData><DeviceConfiguration>` +
		`<MSTPInstanceList type="section">` +
		`<Entry><instanceID>0</instanceID><VLANList>2-4094</VLANList><priority>0</priority></Entry>`
	var e [2]string
	for _, e = range entries {
		out += `<Entry><instanceID>` + e[0] + `</instanceID><VLANList>` + e[1] +
			`</VLANList><priority>32768</priority></Entry>`
	}
	return out + `</MSTPInstanceList></DeviceConfiguration></ResponseData>`
}

func TestCompressVLANRanges(t *testing.T) {
	var cases = []struct {
		in   []int
		want string
	}{
		{nil, ""},
		{[]int{7}, "7"},
		{[]int{1, 2, 3}, "1-3"},
		{[]int{10, 11, 12, 13, 14, 15, 20, 30, 31, 32}, "10-15,20,30-32"},
		// The site VLANs the lab region maps: the exact string the device must receive.
		{[]int{42, 98, 99, 100}, "42,98-100"},
		// Unsorted with duplicates: same request both runs or transcripts mean nothing.
		{[]int{100, 42, 99, 42, 98}, "42,98-100"},
		// Out-of-range IDs are dropped, never sent.
		{[]int{0, -3, 5000, 42}, "42"},
	}
	var c struct {
		in   []int
		want string
	}
	for _, c = range cases {
		var got = compressVLANRanges(c.in)
		if got != c.want {
			t.Errorf("compressVLANRanges(%v) = %q, expected %q", c.in, got, c.want)
		}
	}
}

func TestParseVLANRanges(t *testing.T) {
	var got = parseVLANRanges("1,42,50,98-100")
	if !reflect.DeepEqual(got, []int{1, 42, 50, 98, 99, 100}) {
		t.Errorf("parseVLANRanges live shape = %v", got)
	}
	// Malformed tokens are ignored, not guessed.
	got = parseVLANRanges(" 10 , x, 30-28, 5-6 ,7")
	if !reflect.DeepEqual(got, []int{5, 6, 7, 10}) {
		t.Errorf("parseVLANRanges tolerant = %v", got)
	}
	if parseVLANRanges("") != nil {
		t.Errorf("parseVLANRanges(\"\") must be empty")
	}
}

// TestParseMSTInstanceEntriesLiveShape: the parser against the real device
// response. Instance 0 (the CIST) must be filtered — MSTRegion never lists it.
func TestParseMSTInstanceEntriesLiveShape(t *testing.T) {
	var got = parseMSTInstanceEntries(mstInstanceListLive)
	if len(got) != 1 {
		t.Fatalf("expected 1 instance (the CIST filtered), got %d: %+v", len(got), got)
	}
	var inst, ok = got[1]
	if !ok {
		t.Fatalf("instance 1 missing: %+v", got)
	}
	if !reflect.DeepEqual(inst.VLANs, []int{1, 42, 50, 98, 99, 100}) {
		t.Errorf("instance 1 VLANs = %v", inst.VLANs)
	}
	if inst.Priority != 32768 {
		t.Errorf("instance 1 priority = %d", inst.Priority)
	}
}

// newMSTFake serves successive MSTPInstanceList reads (one body per read, the
// last repeating) and records every write, answering statusCode 0 — or a
// refusal for actions listed in refuse.
func newMSTFake(t *testing.T, seen *[]captured, refuse map[string]bool,
	reads ...string) *httptest.Server {
	t.Helper()
	var idx = 0
	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var body = readAll(r)
			*seen = append(*seen, captured{
				method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, body: body,
			})
			if r.Method == http.MethodGet {
				var i = idx
				if i >= len(reads) {
					i = len(reads) - 1
				}
				idx++
				_, _ = w.Write([]byte(reads[i]))
				return
			}
			var action string
			for action = range refuse {
				if strings.Contains(body, `action="`+action+`"`) {
					_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
						`<ResponseData><ActionStatus><requestURL>wcd</requestURL>` +
						`<statusCode>-3</statusCode><statusString>refused</statusString>` +
						`</ActionStatus></ResponseData>`))
					return
				}
			}
			_, _ = w.Write([]byte(okResponse))
		}))
	t.Cleanup(srv.Close)
	return srv
}

// TestApplyMSTCreatesInstanceWithTheThreeWrites: instance absent → the UI's
// exact sequence, in its order: logical instance add, priority set, VLAN map
// set with the range-compressed list — then a verifying reread.
func TestApplyMSTCreatesInstanceWithTheThreeWrites(t *testing.T) {
	var seen []captured
	var srv = newMSTFake(t, &seen, nil,
		mstInstanceListXML(),                            // before: no instance 1
		mstInstanceListXML([2]string{"1", "42,98-100"}), // after: as written
	)
	var a = adminOn(srv)
	var out driver.ApplyResult
	var err = a.applyMSTInstanceVLANs(context.Background(),
		map[int][]int{1: {42, 98, 99, 100}}, nil, &out)
	if err != nil {
		t.Fatalf("applyMSTInstanceVLANs: %v", err)
	}

	var w = writes(seen)
	if len(w) != 3 {
		t.Fatalf("expected 3 writes, got %d: %+v", len(w), w)
	}
	// (1) the logical instance, gated on statusCode 0 by writeSection.
	if !strings.Contains(w[0].body, `<MSTPLogicalInstanceList action="add">`+
		`<Entry><logicalinstanceID>1</logicalinstanceID></Entry>`) {
		t.Errorf("write 1 = %q", w[0].body)
	}
	if !strings.Contains(w[0].rawQuery, "{MSTPLogicalInstanceList}") {
		t.Errorf("write 1 must carry the ?{Section} query, got %q", w[0].rawQuery)
	}
	// (2) the priority (default 32768 when the plan names none).
	if !strings.Contains(w[1].body, `<MSTPInstanceList action="set">`+
		`<Entry><instanceID>1</instanceID><priority>32768</priority></Entry>`) {
		t.Errorf("write 2 = %q", w[1].body)
	}
	// (3) the map, RANGE-COMPRESSED — the only form the product UI ever sends.
	if !strings.Contains(w[2].body, `<MSTPVLANList action="set">`+
		`<Entry><instanceID>1</instanceID><VLANID>42,98-100</VLANID></Entry>`) {
		t.Errorf("write 3 = %q", w[2].body)
	}
	if len(out.Skipped) != 0 {
		t.Errorf("nothing should be skipped: %v", out.Skipped)
	}
	var joined = strings.Join(out.Notes, " | ")
	if !strings.Contains(joined, "verified by reread") {
		t.Errorf("the result must say the write was verified: %v", out.Notes)
	}
}

// TestApplyMSTWritesNothingWhenTheDeviceAlreadyMatches: read-compare-decide —
// a compliant device receives NO write, and says NOTHING about it.
//
// The silence is the assertion, and it used to be the opposite one. The result
// carried "MST instance 1 already maps 42,98-100 — nothing written", which
// reached the operator in the banner titled "what the device actually carries" —
// a panel that promises to name the cases where a VLAN configuration is in place
// and WITHOUT EFFECT, and on which ports. "The device already agreed with the
// plan" is the nominal outcome of an idempotent apply, not an observation, and
// spending that panel on it is what teaches an operator to skim past the lines
// that do matter. Cf. applyMSTInstanceVLANs.
func TestApplyMSTWritesNothingWhenTheDeviceAlreadyMatches(t *testing.T) {
	var seen []captured
	var srv = newMSTFake(t, &seen, nil,
		mstInstanceListXML([2]string{"1", "42,98-100"}))
	var a = adminOn(srv)
	var out driver.ApplyResult
	var err = a.applyMSTInstanceVLANs(context.Background(),
		map[int][]int{1: {42, 98, 99, 100}}, nil, &out)
	if err != nil {
		t.Fatalf("applyMSTInstanceVLANs: %v", err)
	}
	if len(writes(seen)) != 0 {
		t.Fatalf("compliant device must receive no write: %+v", writes(seen))
	}
	if len(out.Notes) != 0 {
		t.Errorf("a non-event must not be reported: %v", out.Notes)
	}
	// And it is not quietly reclassified as a refusal either: Skipped is the
	// force-able bucket the UI counts as "ports left alone".
	if len(out.Skipped) != 0 {
		t.Errorf("nothing was refused: %v", out.Skipped)
	}
}

// TestApplyMSTReportsWhatTheDeviceHad: the counterpart of the test above, and the
// line that separates a note from an echo.
//
// A device whose instance 1 carries a DIFFERENT VLAN set was in a different MST
// region than the plan — the VLAN-to-instance table is one third of the region
// identity, alongside the name and the revision, and 802.1s demotes a mismatched
// neighbour to ordinary RSTP without logging anything (cf. mdpv1.MSTRegion). So
// the note names what the device HAD, which is a fact no step transcript carries
// and the standard model cannot express. That is why this one survives and the
// "already maps" one does not.
func TestApplyMSTReportsWhatTheDeviceHad(t *testing.T) {
	var seen []captured
	var srv = newMSTFake(t, &seen, nil,
		mstInstanceListXML([2]string{"1", "42,98-100"}),       // before
		mstInstanceListXML([2]string{"1", "10-11,42,98-100"}), // after the set
	)
	var a = adminOn(srv)
	var out driver.ApplyResult
	var err = a.applyMSTInstanceVLANs(context.Background(),
		map[int][]int{1: {10, 11, 42, 98, 99, 100}}, nil, &out)
	if err != nil {
		t.Fatalf("applyMSTInstanceVLANs: %v", err)
	}
	var joined = strings.Join(out.Notes, " | ")
	if !strings.Contains(joined, "device had 42,98-100") {
		t.Errorf("the note must name the PRIOR mapping, the part no step carries: %v",
			out.Notes)
	}
}

// TestApplyMSTRestoresExtraVLANsToCIST: VLANs mapped on the device but absent
// from the plan go back to the CIST via action `restore` — the exact live
// situation of sw-labo-1930, whose instance 1 carried 1 and 50 beyond the plan.
func TestApplyMSTRestoresExtraVLANsToCIST(t *testing.T) {
	var seen []captured
	var srv = newMSTFake(t, &seen, nil,
		mstInstanceListXML([2]string{"1", "1,42,50,98-100"}), // before
		mstInstanceListXML([2]string{"1", "1,42,50,98-100"}), // after set: extras remain
		mstInstanceListXML([2]string{"1", "42,98-100"}),      // after restore
	)
	var a = adminOn(srv)
	var out driver.ApplyResult
	var err = a.applyMSTInstanceVLANs(context.Background(),
		map[int][]int{1: {42, 98, 99, 100}}, nil, &out)
	if err != nil {
		t.Fatalf("applyMSTInstanceVLANs: %v", err)
	}

	var w = writes(seen)
	if len(w) != 2 {
		t.Fatalf("expected set + restore, got %d writes: %+v", len(w), w)
	}
	// The FULL desired list, not just the missing VLANs: correct whether `set`
	// maps rows or replaces membership.
	if !strings.Contains(w[0].body, `<MSTPVLANList action="set">`+
		`<Entry><instanceID>1</instanceID><VLANID>42,98-100</VLANID></Entry>`) {
		t.Errorf("set write = %q", w[0].body)
	}
	if !strings.Contains(w[1].body, `<MSTPVLANList action="restore">`+
		`<Entry><instanceID>1</instanceID><VLANID>1,50</VLANID></Entry>`) {
		t.Errorf("restore write = %q", w[1].body)
	}
	if !strings.Contains(strings.Join(out.Notes, " "), "restored to the CIST") {
		t.Errorf("notes must state the restore: %v", out.Notes)
	}
	if !strings.Contains(strings.Join(out.Notes, " "), "verified by reread") {
		t.Errorf("the final state must be verified: %v", out.Notes)
	}
}

// TestApplyMSTSaysWhenRestoreIsRefused: `restore` is the one action read from
// the JS but never proven on hardware first. A refusal must land in Skipped
// with the VLANs still mapped — not fail the apply, not stay quiet.
func TestApplyMSTSaysWhenRestoreIsRefused(t *testing.T) {
	var seen []captured
	var srv = newMSTFake(t, &seen, map[string]bool{"restore": true},
		mstInstanceListXML([2]string{"1", "1,42,50,98-100"}),
		mstInstanceListXML([2]string{"1", "1,42,50,98-100"}),
	)
	var a = adminOn(srv)
	var out driver.ApplyResult
	var err = a.applyMSTInstanceVLANs(context.Background(),
		map[int][]int{1: {42, 98, 99, 100}}, nil, &out)
	if err != nil {
		t.Fatalf("a refused restore must not fail the apply: %v", err)
	}
	var joined = strings.Join(out.Skipped, " | ")
	if !strings.Contains(joined, "restore-to-CIST write was refused") ||
		!strings.Contains(joined, "1,50") {
		t.Errorf("Skipped must carry the refusal and the VLANs still mapped: %v", out.Skipped)
	}
}

// TestApplyMSTDetectsTheSilentFailure: the platform's signature failure is
// statusCode 0 with no effect. A write that does not read back must be SAID.
func TestApplyMSTDetectsTheSilentFailure(t *testing.T) {
	var seen []captured
	var srv = newMSTFake(t, &seen, nil,
		mstInstanceListXML(), // before: nothing
		mstInstanceListXML(), // after: still nothing — the device lied
	)
	var a = adminOn(srv)
	var out driver.ApplyResult
	var err = a.applyMSTInstanceVLANs(context.Background(),
		map[int][]int{1: {42, 98, 99, 100}}, nil, &out)
	if err != nil {
		t.Fatalf("applyMSTInstanceVLANs: %v", err)
	}
	if !strings.Contains(strings.Join(out.Skipped, " "), "does not read back") {
		t.Errorf("a silent failure must be reported: %v", out.Skipped)
	}
}

// TestApplyMSTLeavesUnplannedInstancesAndSaysSo: an instance the plan does not
// name is not deleted (that shape is unobserved) — but the divergence is stated.
func TestApplyMSTLeavesUnplannedInstancesAndSaysSo(t *testing.T) {
	var seen []captured
	var srv = newMSTFake(t, &seen, nil,
		mstInstanceListXML([2]string{"1", "42,98-100"}, [2]string{"2", "200"}))
	var a = adminOn(srv)
	var out driver.ApplyResult
	var err = a.applyMSTInstanceVLANs(context.Background(),
		map[int][]int{1: {42, 98, 99, 100}}, nil, &out)
	if err != nil {
		t.Fatalf("applyMSTInstanceVLANs: %v", err)
	}
	if len(writes(seen)) != 0 {
		t.Fatalf("no write expected: %+v", writes(seen))
	}
	var joined = strings.Join(out.Skipped, " ")
	if !strings.Contains(joined, "MST instance 2 exists on the device but not in the plan") {
		t.Errorf("the unplanned instance must be reported: %v", out.Skipped)
	}
}
