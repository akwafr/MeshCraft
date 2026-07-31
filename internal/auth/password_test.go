package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	var hash, err = HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Error("correct password must be accepted")
	}
	if VerifyPassword("wrong password", hash) {
		t.Error("wrong password must be rejected")
	}
	var hash2, err2 = HashPassword("correct horse battery staple")
	if err2 != nil {
		t.Fatalf("HashPassword: %v", err2)
	}
	if hash == hash2 {
		t.Error("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	var cases = []string{"", "$argon2id$", "not-a-hash", "$argon2i$v=19$m=1,t=1,p=1$AA$AA"}
	var c string
	for _, c = range cases {
		if VerifyPassword("x", c) {
			t.Errorf("malformed hash accepted: %q", c)
		}
	}
}
