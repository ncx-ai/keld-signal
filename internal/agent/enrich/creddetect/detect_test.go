package creddetect

import "testing"

func TestDetectFindsKnownCreds(t *testing.T) {
	cases := []string{
		"deploy with aws key AKIAIOSFODNN7EXAMPLE and go",
		"here's the token ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"use stripe sk_live_4eC39HqLyjWDarjtT1zdp7dc for billing",
	}
	for _, c := range cases {
		if len(Detect(c)) == 0 {
			t.Errorf("expected a credential span in %q", c)
		}
	}
}

func TestDetectSkipsDecoys(t *testing.T) {
	// a git SHA and a UUID must NOT match a credential rule.
	for _, c := range []string{
		"the deploy commit is a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		"order id 550e8400-e29b-41d4-a716-446655440000 shipped",
	} {
		if s := Detect(c); len(s) != 0 {
			t.Errorf("decoy %q wrongly matched %+v", c, s)
		}
	}
}
