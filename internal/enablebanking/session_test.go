package enablebanking

import (
	"encoding/json"
	"testing"
)

func TestSessionDecodeAccessValidUntil(t *testing.T) {
	body := `{"session_id":"s1","accounts":[],"aspsp":{"name":"PKO Bank Polski","country":"PL"},"psu_type":"business","access":{"valid_until":"2027-05-17T08:12:00.000000+00:00"}}`
	var s Session
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := "2027-05-17T08:12:00.000000+00:00"; s.Access.ValidUntil != want {
		t.Fatalf("valid_until = %q, want %q", s.Access.ValidUntil, want)
	}
}
