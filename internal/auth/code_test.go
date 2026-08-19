package auth

import "testing"

func TestParsePairingCode(t *testing.T) {
	cases := []struct {
		name, in, wantBase, wantCode string
		wantErr                      bool
	}{
		{name: "bare code", in: "ABCD-EFGH", wantBase: "", wantCode: "ABCD-EFGH"},
		{name: "bare host", in: "atlas.keld.co/ABCD-EFGH", wantBase: "https://atlas.keld.co", wantCode: "ABCD-EFGH"},
		{name: "explicit https", in: "https://atlas.keld.co/ABCD-EFGH", wantBase: "https://atlas.keld.co", wantCode: "ABCD-EFGH"},
		{name: "explicit http", in: "http://dev.example/ABCD-EFGH", wantBase: "http://dev.example", wantCode: "ABCD-EFGH"},
		{name: "localhost with port", in: "localhost:8000/ABCD-EFGH", wantBase: "http://localhost:8000", wantCode: "ABCD-EFGH"},
		{name: "loopback ip", in: "127.0.0.1:8000/ABCD-EFGH", wantBase: "http://127.0.0.1:8000", wantCode: "ABCD-EFGH"},
		{name: "path bearing host", in: "https://example.com/keld/ABCD-EFGH", wantBase: "https://example.com/keld", wantCode: "ABCD-EFGH"},
		{name: "lowercase code is uppercased", in: "abcd-efgh", wantBase: "", wantCode: "ABCD-EFGH"},
		{name: "lowercase code with host", in: "atlas.keld.co/abcd-efgh", wantBase: "https://atlas.keld.co", wantCode: "ABCD-EFGH"},
		{name: "surrounding whitespace", in: "  atlas.keld.co/ABCD-EFGH \n", wantBase: "https://atlas.keld.co", wantCode: "ABCD-EFGH"},
		{name: "empty", in: "", wantErr: true},
		{name: "whitespace only", in: "   ", wantErr: true},
		{name: "trailing slash", in: "atlas.keld.co/", wantErr: true},
		{name: "trailing slash after code", in: "atlas.keld.co/ABCD-EFGH/", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, code, err := ParsePairingCode(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParsePairingCode(%q) = (%q, %q, nil), want error", tc.in, base, code)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePairingCode(%q): %v", tc.in, err)
			}
			if base != tc.wantBase || code != tc.wantCode {
				t.Fatalf("ParsePairingCode(%q) = (%q, %q), want (%q, %q)", tc.in, base, code, tc.wantBase, tc.wantCode)
			}
		})
	}
}
