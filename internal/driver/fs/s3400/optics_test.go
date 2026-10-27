package s3400

// The optics parser, replayed on the CAPTURED per-interface answers
// (testdata/show_interface_*.txt, lab 48T4SP, 2026-08-05). What these tests
// hold still: the irregular measurement line ("supply voltage :3.26V",
// "Bias Current.:35.61mA") keeps parsing, an empty cage and a copper port
// keep reading as "no module" rather than as a module with no readings, and
// the threshold table's look-alike labels ("TX power(dBm):") never bleed
// into the readings.

import (
	"os"
	"path/filepath"
	"testing"
)

func readCapture(t *testing.T, name string) string {
	t.Helper()
	var b, err = os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("capture %s: %v", name, err)
	}
	return string(b)
}

func TestParseInterfaceOpticsPopulatedCage(t *testing.T) {
	var st, served = parseInterfaceOptics(readCapture(t, "show_interface_tg01.txt"))
	if !served {
		t.Fatal("tg0/1 carries a module and a full DDM block — served must be true")
	}
	if !st.DDM {
		t.Error("DDM:YES on the wire, parsed false")
	}
	if st.Vendor != "FS" || st.PartNumber != "SFP-10G-BX" || st.Serial != "G2210006755" {
		t.Errorf("module identity = %q/%q/%q, want FS/SFP-10G-BX/G2210006755",
			st.Vendor, st.PartNumber, st.Serial)
	}
	var checks = []struct {
		name string
		got  *float64
		want float64
	}{
		{"tx_power_dbm", st.TxPowerDBm, -2.08},
		{"rx_power_dbm", st.RxPowerDBm, -1.85},
		{"temperature_c", st.TemperatureC, 46.00},
		{"voltage_v", st.VoltageV, 3.26},
		{"bias_current_ma", st.BiasCurrentMA, 35.61},
	}
	var ck = checks[0]
	for _, ck = range checks {
		if ck.got == nil {
			t.Errorf("%s: nil, want %v", ck.name, ck.want)
			continue
		}
		if *ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, *ck.got, ck.want)
		}
	}
}

// The second populated cage carries a DIFFERENT vendor's module with its own
// values — the proof the parser reads the device's bytes, not one module's.
func TestParseInterfaceOpticsSecondCage(t *testing.T) {
	var st, served = parseInterfaceOptics(readCapture(t, "show_interface_tg02.txt"))
	if !served {
		t.Fatal("tg0/2 carries a module — served must be true")
	}
	if st.Vendor != "Pureoptics" || st.PartNumber != "SFP-10G-LR-BXD" || st.Serial != "KAFDH587" {
		t.Errorf("module identity = %q/%q/%q, want Pureoptics/SFP-10G-LR-BXD/KAFDH587",
			st.Vendor, st.PartNumber, st.Serial)
	}
	if st.TxPowerDBm == nil || *st.TxPowerDBm != -1.52 {
		t.Errorf("tx_power_dbm = %v, want -1.52", st.TxPowerDBm)
	}
	if st.RxPowerDBm == nil || *st.RxPowerDBm != -1.55 {
		t.Errorf("rx_power_dbm = %v, want -1.55", st.RxPowerDBm)
	}
}

func TestParseInterfaceOpticsEmptyCageAndCopper(t *testing.T) {
	var name string
	for _, name = range []string{
		"show_interface_tg03.txt", // empty cage: "Hardware is 10Giga-FX", no block
		"show_interface_tg04.txt",
		"show_interface_g01.txt", // copper: never a transceiver block
	} {
		var st, served = parseInterfaceOptics(readCapture(t, name))
		if served {
			t.Errorf("%s: served=true on an output with no transceiver block (%+v)", name, st)
		}
	}
}

// The 2026-08-03 hand capture had a SPACE after the comma ("C, supply")
// where the 2026-08-05 one has none — the parser must take both spellings,
// and a same-line variant with every separator loosened.
func TestParseInterfaceOpticsSeparatorTolerance(t *testing.T) {
	var out = "Transceiver Info:\n" +
		"    DDM:YES,Vend:FS,PN:SFP-10G-BX\n" +
		"DDM info:\n" +
		"    TX power:-1.61 dBm, RX power:-1.51 dBm\n" +
		"    SFP temperature:47.00 C, supply voltage :3.29V, Bias Current.:42.84mA\n"
	var st, served = parseInterfaceOptics(out)
	if !served {
		t.Fatal("served must be true")
	}
	if st.TxPowerDBm == nil || *st.TxPowerDBm != -1.61 {
		t.Errorf("tx = %v, want -1.61", st.TxPowerDBm)
	}
	if st.TemperatureC == nil || *st.TemperatureC != 47.00 {
		t.Errorf("temp = %v, want 47", st.TemperatureC)
	}
	if st.VoltageV == nil || *st.VoltageV != 3.29 {
		t.Errorf("voltage = %v, want 3.29", st.VoltageV)
	}
	if st.BiasCurrentMA == nil || *st.BiasCurrentMA != 42.84 {
		t.Errorf("bias = %v, want 42.84", st.BiasCurrentMA)
	}
}
