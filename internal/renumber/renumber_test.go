package renumber

import "testing"

func TestRememberAndHas(t *testing.T) {
	var tr = NewTracker()
	tr.Remember("dev1", "site1", "sw1", "192.0.2.10", "", "192.0.2.10")
	if !tr.Has("dev1", "192.0.2.10") {
		t.Fatal("expected candidate remembered")
	}
	if tr.Has("dev1", "192.0.2.11") {
		t.Fatal("unexpected candidate")
	}
	var all = tr.All()
	if len(all) != 1 || all[0].IP != "192.0.2.10" {
		t.Fatalf("All = %+v", all)
	}
	tr.ForgetIP("dev1", "192.0.2.10")
	if tr.Has("dev1", "192.0.2.10") {
		t.Fatal("ForgetIP should clear")
	}
}

func TestForgetClearsDevice(t *testing.T) {
	var tr = NewTracker()
	tr.Remember("dev1", "site1", "sw1", "10.0.0.1", "10.0.0.2")
	tr.Forget("dev1")
	if len(tr.All()) != 0 {
		t.Fatalf("Forget should clear all candidates: %+v", tr.All())
	}
}
