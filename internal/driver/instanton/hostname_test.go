package instanton

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The system-name write, tested against a fake device — because the SECTION NAME
// IS NOT CAPTURED (cf. hostname.go) and the only thing that can be pinned here is
// the MECHANISM: probe the candidates, write only into a section and a field the
// device just served, refuse when none answers, and catch the code-0-that-changed-
// nothing. The XML shapes served below are CONSTRUCTED in this firmware's observed
// style, not captured from a 1930.

// sectionAbsent is what this interface answers for a section it does not know
// (the wording sectionMissing keys on).
const sectionAbsent = `<?xml version="1.0" encoding="UTF-8"?><ResponseData>` +
	`<ActionStatus><statusCode>-1</statusCode>` +
	`<statusString>Cannot find the section</statusString></ActionStatus></ResponseData>`

// newSysNameSwitch serves ONE section carrying ONE name field, records every
// request, and — when takes is true — stores what a write sends so the re-read
// sees it. takes=false is this platform's silent failure: code 0, nothing changed.
func newSysNameSwitch(t *testing.T, seen *[]captured, section, field, initial string,
	takes bool) *httptest.Server {
	t.Helper()
	var value = initial
	var reValue = regexp.MustCompile(`(?is)<` + regexp.QuoteMeta(field) + `>(.*?)</` +
		regexp.QuoteMeta(field) + `>`)
	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var raw, _ = io.ReadAll(r.Body)
			*seen = append(*seen, captured{
				method:      r.Method,
				path:        r.URL.Path,
				rawQuery:    r.URL.RawQuery,
				contentType: r.Header.Get("Content-Type"),
				body:        string(raw),
			})
			if r.Method == http.MethodGet {
				if !strings.Contains(r.URL.RawQuery, "{"+section+"}") {
					_, _ = w.Write([]byte(sectionAbsent))
					return
				}
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
					`<DeviceConfiguration><` + section + `>` +
					`<` + field + `>` + value + `</` + field + `>` +
					`<sysLocation>Lab</sysLocation>` +
					`</` + section + `></DeviceConfiguration>`))
				return
			}
			if takes {
				var m = reValue.FindStringSubmatch(string(raw))
				if m != nil {
					value = m[1]
				}
			}
			_, _ = w.Write([]byte(okResponse))
		}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSetHostnameWritesTheSectionThatAnswered(t *testing.T) {
	// The section probed here is the SECOND candidate on purpose: the first must be
	// tried, found absent, and abandoned without a write.
	var seen []captured
	var srv = newSysNameSwitch(t, &seen, "SystemGlobalSetting", "sysName", "IO-4A5B6C", true)

	var err = adminOn(srv).SetHostname(context.Background(), "sw-lab-01")
	if err != nil {
		t.Fatalf("SetHostname: %v", err)
	}

	// Exactly one POST may carry the name, and it must go to the section that
	// answered — with the query string, the DeviceConfiguration wrapper and the
	// action as an ATTRIBUTE, the three conventions without which this device
	// accepts a request and does nothing.
	var writes []captured
	var saves []captured
	var c captured
	for _, c = range seen {
		if c.method != http.MethodPost {
			continue
		}
		if strings.Contains(c.rawQuery, "FileCopy") {
			saves = append(saves, c)
			continue
		}
		writes = append(writes, c)
	}
	if len(writes) != 1 {
		t.Fatalf("expected exactly one section write, got %d (%+v)", len(writes), writes)
	}
	if writes[0].rawQuery != "{SystemGlobalSetting}" {
		t.Errorf("query = %q, want {SystemGlobalSetting}", writes[0].rawQuery)
	}
	if !strings.Contains(writes[0].body, `<SystemGlobalSetting action="set">`) {
		t.Errorf("action must be a section attribute: %s", writes[0].body)
	}
	if !strings.Contains(writes[0].body, "<sysName>sw-lab-01</sysName>") {
		t.Errorf("body must carry the field the device served: %s", writes[0].body)
	}
	if !strings.Contains(writes[0].body, "<DeviceConfiguration>") {
		t.Errorf("body must be wrapped in DeviceConfiguration: %s", writes[0].body)
	}
	// And it must be SAVED: this range is apply_then_save, so an unsaved name is a
	// name the next power cut removes.
	if len(saves) != 1 {
		t.Fatalf("expected one FileCopy save, got %d", len(saves))
	}
	if !strings.Contains(saves[0].body, "<srcFileType>2</srcFileType>") ||
		!strings.Contains(saves[0].body, "<destFileType>3</destFileType>") {
		t.Errorf("save must copy running → startup: %s", saves[0].body)
	}
}

func TestSetHostnameRefusesWhenNoSectionAnswers(t *testing.T) {
	// The case this whole file is shaped around: the section name has never been
	// captured, so "none of them answered" is a real outcome and it must be a
	// refusal that names every candidate — never a POST to a guessed name, which on
	// this platform answers code 0 and changes nothing.
	var seen []captured
	var srv = newSysNameSwitch(t, &seen, "NoSuchSection", "sysName", "IO-4A5B6C", true)

	var err = adminOn(srv).SetHostname(context.Background(), "sw-lab-01")
	if err == nil {
		t.Fatal("a device serving no system-name section must be refused")
	}
	var name string
	for _, name = range systemNameSections {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the refusal must name candidate %q: %v", name, err)
		}
	}
	var c captured
	for _, c = range seen {
		if c.method == http.MethodPost {
			t.Errorf("nothing may be written when no section answered, got POST %q", c.rawQuery)
		}
	}
}

func TestSetHostnameRefusesASectionWithoutAKnownField(t *testing.T) {
	// A section that answers but carries none of the aliases is NOT good enough:
	// writing an invented field into a real section is the same silent failure as
	// writing to an invented section, and harder to notice because the section name
	// looked right.
	var seen []captured
	var srv = newSysNameSwitch(t, &seen, "SystemSetting", "sysContact", "someone", true)

	var err = adminOn(srv).SetHostname(context.Background(), "sw-lab-01")
	if err == nil {
		t.Fatal("a section with no system-name field must be refused")
	}
	var c captured
	for _, c = range seen {
		if c.method == http.MethodPost {
			t.Errorf("nothing may be written into a section whose field set is unknown, "+
				"got POST %q", c.rawQuery)
		}
	}
}

func TestSetHostnameCatchesTheCodeZeroThatChangedNothing(t *testing.T) {
	// takes=false: the device answers statusCode 0 and keeps its old name. This is
	// the failure mode this platform is known for, and the re-read is the only thing
	// that catches it — nothing may be saved to flash afterwards.
	var seen []captured
	var srv = newSysNameSwitch(t, &seen, "SystemSetting", "sysName", "IO-4A5B6C", false)

	var err = adminOn(srv).SetHostname(context.Background(), "sw-lab-01")
	if err == nil {
		t.Fatal("a write the device ignored must not be reported as done")
	}
	if !strings.Contains(err.Error(), "IO-4A5B6C") {
		t.Errorf("the refusal must say what the device still carries: %v", err)
	}
	var c captured
	for _, c = range seen {
		if c.method == http.MethodPost && strings.Contains(c.rawQuery, "FileCopy") {
			t.Error("an unconfirmed write must not be saved to flash")
		}
	}
}

func TestSetHostnameSendsNothingWhenTheNameIsAlreadyRight(t *testing.T) {
	// A POST that changes nothing is a risk taken for nothing on this interface,
	// and it would also spend the flash write that follows it.
	var seen []captured
	var srv = newSysNameSwitch(t, &seen, "SystemSetting", "sysName", "sw-lab-01", true)

	var err = adminOn(srv).SetHostname(context.Background(), "sw-lab-01")
	if err != nil {
		t.Fatalf("SetHostname: %v", err)
	}
	var c captured
	for _, c = range seen {
		if c.method == http.MethodPost {
			t.Errorf("no write may be sent when the name is already right, got POST %q",
				c.rawQuery)
		}
	}
}

func TestSetHostnameRefusesAnInvalidNameBeforeAnyRequest(t *testing.T) {
	// An invalid name must not cost a round trip on a device that grants five
	// sessions in total.
	var seen []captured
	var srv = newSysNameSwitch(t, &seen, "SystemSetting", "sysName", "IO-4A5B6C", true)

	var err = adminOn(srv).SetHostname(context.Background(), "sw lab 01")
	if err == nil {
		t.Fatal("a name outside the RFC 1123 alphabet must be refused")
	}
	if len(seen) != 0 {
		t.Errorf("validation must happen before the device is reached, saw %d requests",
			len(seen))
	}
}

func TestHostnameMismatchCarriesThePreviousName(t *testing.T) {
	var empty = hostnameMismatch("sw-lab-01", "", "IO-4A5B6C")
	if !strings.Contains(empty, "IO-4A5B6C") || !strings.Contains(empty, "code 0") {
		t.Errorf("an empty re-read must name the code-0 failure and the old name, got %q", empty)
	}
	var cut = hostnameMismatch("sw-lab-01-very-long", "sw-lab-01-very-", "IO-4A5B6C")
	if !strings.Contains(cut, "15 bytes") {
		t.Errorf("a truncation must name the observed ceiling, got %q", cut)
	}
	var other = hostnameMismatch("sw-lab-01", "something-else", "IO-4A5B6C")
	if !strings.Contains(other, `"something-else"`) {
		t.Errorf("a divergence must show what the device carries, got %q", other)
	}
}
