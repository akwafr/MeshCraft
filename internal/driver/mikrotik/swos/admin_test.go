package swos

import (
	"strings"
	"testing"
)

// The SwOS administration password, and the homemade digest that carries its secret.
//
// This is the only place in this connector where a write is verified by a SECOND
// implementation rather than by a re-read: the password algorithm is too
// specific — and its failure too costly, this model having no serial port — to
// settle for checking the code against itself.

// TestPasswordDigest: the algorithm, verified through a SECOND path.
//
// The expected values are not the ones this code produces: they were
// computed by a LITERAL reimplementation in Python of the `sa` and `ra`
// functions from the device's JavaScript, independent of the Go implementation. This
// is what gives the test its value — checking a piece of code against itself proves
// nothing, and this write could never be exercised on real hardware.
//
// Two cases carry all the risk:
//
//   - the EMPTY old password, which is the factory state and that of the reference
//     device. The RC4 key then becomes "*", and without that substitution RC4 would
//     have no key;
//   - the SIXTY-FOUR stream bytes applied over thirty-two, i.e. two passes.
//     Stopping at 32 produces a plausible-looking digest that the device rejects.
func TestPasswordDigest(t *testing.T) {
	var cases = []struct {
		nouveau, ancien, expected string
	}{
		{
			// The case of a device fresh out of the factory: empty old secret.
			"MeshCraft2026", "",
			"6459d8580c55190317531ec3ef2264ad0f479a5109cdc8e40d14998fdcbef5df",
		},
		{
			// GOLDEN VECTOR — do not "translate" these inputs. The expected
			// digest was computed independently from the device's own
			// JavaScript for these exact strings; changing the input without
			// recomputing the digest turns a cryptographic check into a
			// permanent false failure.
			"nouveau", "ancien",
			"a170b4d0aac65e145e6c4ecf40b14412e14c4d32ad24efcd2fcdd3724a17b19c",
		},
		{
			// Both empty: clearing the password of a brand-new device.
			"", "",
			"6414bd2b64166b6271272cf3dd1464ad0f479a5109cdc8e40d14998fdcbef5df",
		},
		{
			"aB3", "xY",
			"07e8c5bbc5786ddd0452b367ebfb4c7953b5db5c0c1aedbf727f36d0c6cc7d7b",
		},
	}
	var c = cases[0]
	for _, c = range cases {
		var got, err = PasswordBlob(c.nouveau, c.ancien)
		if err != nil {
			t.Errorf("PasswordBlob(%q, %q): %v", c.nouveau, c.ancien, err)
			continue
		}
		if got != c.expected {
			t.Errorf("PasswordBlob(%q, %q) =\n  %s\nexpected (independent computation from the "+
				"device's JavaScript):\n  %s", c.nouveau, c.ancien, got, c.expected)
		}
		// The digest is always 32 bytes, so 64 hexadecimal characters.
		if len(got) != 64 {
			t.Errorf("digest of %d characters, expected 64", len(got))
		}
	}
}

// TestPasswordRefusedBeforeSending: refuse here, not on the device.
//
// Both constraints are observed in the device's interface, and both cost a
// trip if ignored: this model has no serial port, and an
// applied secret that isn't the one intended is not recoverable.
func TestPasswordRefusedBeforeSending(t *testing.T) {
	// More than 15 characters: the interface refuses ("New password is too long").
	var _, err = PasswordBlob("0123456789abcdef", "")
	if err == nil {
		t.Error("a 16-character secret was accepted")
	}
	if err != nil && !strings.Contains(err.Error(), "15") {
		t.Errorf("the refusal must cite the device's limit; got: %v", err)
	}
	// The old secret is bounded the same way ("Bad old password").
	if _, err = PasswordBlob("short", "0123456789abcdef"); err == nil {
		t.Error("a 16-character old secret was accepted")
	}

	// Non-ASCII: the device's interface builds its bytes with
	// `charCodeAt`, which returns UTF-16 code units. Beyond 127, what a
	// browser would send and what Go would send diverge, and the applied secret isn't
	// the one intended.
	if _, err = PasswordBlob("pässwörd", ""); err == nil {
		t.Error("a non-ASCII secret was accepted: the applied password would not be " +
			"the one intended, and this platform has no rescue console")
	}
	if err != nil && !strings.Contains(strings.ToUpper(err.Error()), "ASCII") {
		t.Errorf("the refusal must name the constraint; got: %v", err)
	}
	// ASCII punctuation, though, goes through: the constraint isn't alphanumeric.
	if _, err = PasswordBlob("a!B#c$3", ""); err != nil {
		t.Errorf("ASCII punctuation should be accepted: %v", err)
	}
	// And the capability says the same thing as the code.
	var admin = (&Driver{}).Capabilities("CSS326-24G-2S+").Admin
	if admin.AlphanumericOnly {
		t.Error("AlphanumericOnly is false on this platform: the constraint is ASCII")
	}
	if admin.PasswordMax != 15 {
		t.Errorf("PasswordMax = %d, the device's interface refuses beyond 15",
			admin.PasswordMax)
	}
}

// TestFixedAccountOnSwOS: there is nothing to name, and that's a fact.
//
// SwOS has no notion of an account: the password-change form
// carries only three fields, the page that receives it only one key, and the
// ENTIRE configuration backup of the device carries no username (verified in
// TestRealBackup).
//
// Consequence for meshcraft: the fleet's unified name can NOT be applied here, and
// it's EffectiveUsername that must resolve it. Checking this here avoids a
// provisioning run storing in the database an account name that doesn't exist on
// any device.
func TestFixedAccountOnSwOS(t *testing.T) {
	var admin = (&Driver{}).Capabilities("CSS326-24G-2S+").Admin
	if admin.Custom {
		t.Error("SwOS cannot create a named account")
	}
	if admin.FixedName != "admin" {
		t.Errorf("FixedName = %q, expected \"admin\"", admin.FixedName)
	}
	// This is what the caller must actually store, whatever it wishes for.
	var got = admin.EffectiveUsername("mcadmin")
	if got != "admin" {
		t.Errorf("EffectiveUsername(\"mcadmin\") = %q; the fleet's unified name isn't "+
			"settable on SwOS, and it's \"admin\" that lands on the device", got)
	}
	// An EMPTY password is the factory state, and it must remain declarable: refusing
	// zero would prevent adopting a brand-new CSS.
	if admin.PasswordMin != 0 {
		t.Errorf("PasswordMin = %d; SwOS accepts an empty secret, observed on "+
			"the reference device", admin.PasswordMin)
	}
}
