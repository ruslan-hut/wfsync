package wfirma

import (
	"encoding/json"
	"testing"
)

// TestIsKSefAuthError ensures the KSeF-authorization detector fires only on the
// authorization error that the draft fallback is meant to recover from, and not on
// unrelated validation errors that happen to mention KSeF or authorization.
func TestIsKSefAuthError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{
			name: "real ksef authorization error",
			msg:  "Brak autoryzacji w KSeF 2.0. Zautoryzuj się w zakładce PRZYCHODY » KSEF I INTEGRACJE",
			want: true,
		},
		{
			name: "version-agnostic wording",
			msg:  "Brak autoryzacji w KSeF. Zautoryzuj się.",
			want: true,
		},
		{
			name: "ksef without authorization wording",
			msg:  "Faktura odrzucona przez KSeF: błąd struktury",
			want: false,
		},
		{
			name: "authorization without ksef",
			msg:  "Brak autoryzacji do tej operacji",
			want: false,
		},
		{
			name: "ordinary validation error",
			msg:  "nip: This field is required.",
			want: false,
		},
		{
			name: "empty",
			msg:  "",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isKSefAuthError(tc.msg); got != tc.want {
				t.Errorf("isKSefAuthError(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// TestClassifyKSefFields verifies the KSeF-readiness gate that decides whether the full
// invoice PDF is downloadable yet or wFirma would still hand back a transaction confirmation.
func TestClassifyKSefFields(t *testing.T) {
	fields := func(m map[string]string) map[string]json.RawMessage {
		out := make(map[string]json.RawMessage, len(m))
		for k, v := range m {
			b, _ := json.Marshal(v)
			out[k] = b
		}
		return out
	}

	cases := []struct {
		name        string
		fields      map[string]json.RawMessage
		wantReady   bool
		wantPending bool
	}{
		{
			name:      "non-ksef invoice (no ksef fields) downloads immediately",
			fields:    fields(map[string]string{"id": "123", "fullnumber": "FV 1/2026", "total": "100"}),
			wantReady: false, wantPending: false,
		},
		{
			name:      "processed: ksef reference number assigned",
			fields:    fields(map[string]string{"id": "123", "ksef_reference_number": "5273103291-20260715-3D8A71800003-34", "ksef_status": "ok"}),
			wantReady: true, wantPending: false,
		},
		{
			name:      "processed: only registration date present",
			fields:    fields(map[string]string{"id": "123", "ksef_registration_date": "2026-07-15 08:45:07"}),
			wantReady: true, wantPending: false,
		},
		{
			name:      "processed: status ok, no number yet parsed",
			fields:    fields(map[string]string{"id": "123", "ksef_status": "ok", "ksef_reference_number": ""}),
			wantReady: true, wantPending: false,
		},
		{
			name:        "pending: submitted, still processing",
			fields:      fields(map[string]string{"id": "123", "ksef_status": "processing", "ksef_reference_number": "", "ksef_registration_date": ""}),
			wantReady:   false,
			wantPending: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ready, pending := classifyKSefFields(tc.fields)
			if ready != tc.wantReady || pending != tc.wantPending {
				t.Errorf("classifyKSefFields = (ready=%v, pending=%v), want (ready=%v, pending=%v)",
					ready, pending, tc.wantReady, tc.wantPending)
			}
		})
	}
}

// TestRawJSONString covers wFirma's inconsistent scalar quoting.
func TestRawJSONString(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`"ok"`, "ok"},
		{`"  spaced  "`, "  spaced  "},
		{`123`, "123"},
		{`null`, ""},
		{``, ""},
	}
	for _, tc := range cases {
		if got := rawJSONString(json.RawMessage(tc.in)); got != tc.want {
			t.Errorf("rawJSONString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
