package wfirma

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"wfsync/entity"
)

// TestParseContractorAdd pins how a contractors/add response is classified: a field-level
// rejection carries wFirma's field and text as a CustomerDataError, anything else stays a
// plain (retryable) error.
func TestParseContractorAdd(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantID    string
		wantField string
		wantMsg   string
		wantErr   string
	}{
		{
			name:   "created",
			body:   `{"contractors":{"0":{"contractor":{"id":"123","errors":false}}},"status":{"code":"OK"}}`,
			wantID: "123",
		},
		{
			name:      "invalid email as object",
			body:      `{"contractors":{"0":{"contractor":{"id":"","errors":{"0":{"error":{"field":"email","message":"Nieprawidłowy adres e-mail.","method":{"name":"email","parameters":""}}}}}}},"status":{"code":"ERROR"}}`,
			wantField: "email",
			wantMsg:   "Nieprawidłowy adres e-mail.",
			wantErr:   "wfirma rejected email: Nieprawidłowy adres e-mail.",
		},
		{
			name:      "errors as array, lowest key wins",
			body:      `{"contractors":{"0":{"contractor":{"errors":[{"error":{"field":"zip","message":"bad zip"}},{"error":{"field":"nip","message":"bad nip"}}]}}},"status":{"code":"ERROR"}}`,
			wantField: "zip",
			wantMsg:   "bad zip",
			wantErr:   "wfirma rejected zip: bad zip",
		},
		{
			name:    "field-less error stays retryable",
			body:    `{"contractors":{"0":{"contractor":{"errors":{"0":{"error":{"field":"","message":"Limit przekroczony"}}}}}},"status":{"code":"ERROR"}}`,
			wantErr: "wfirma error: Limit przekroczony",
		},
		{
			name:    "error without field details",
			body:    `{"status":{"code":"ERROR","message":"Wewnętrzny błąd"}}`,
			wantErr: "wfirma error: Wewnętrzny błąd",
		},
		{
			name:    "ok without id",
			body:    `{"contractors":{"0":{"contractor":{}}},"status":{"code":"OK"}}`,
			wantErr: "no contractor id returned",
		},
		{
			name:    "not json",
			body:    `<html>`,
			wantErr: "parse contractor response: invalid character '<' looking for beginning of value",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := parseContractorAdd([]byte(tc.body))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if id != tc.wantID {
					t.Errorf("id = %q, want %q", id, tc.wantID)
				}
				return
			}
			if err == nil {
				t.Fatalf("got id %q, want error %q", id, tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("error = %q, want %q", err, tc.wantErr)
			}
			cde := entity.AsCustomerDataError(err)
			if tc.wantField == "" {
				if cde != nil {
					t.Errorf("got CustomerDataError %v, want a retryable error", cde)
				}
				return
			}
			if cde == nil {
				t.Fatalf("error %v is not a CustomerDataError", err)
			}
			if cde.Field != tc.wantField || cde.Message != tc.wantMsg {
				t.Errorf("got %s/%q, want %s/%q", cde.Field, cde.Message, tc.wantField, tc.wantMsg)
			}
		})
	}
}

// Contractor 200295636 (.com order 18463) was stored as nip "HUCHE266473703", type
// "custom": the Swiss UID glued to the Hungarian address country. The next order
// carrying the same UID must clear it rather than fall back to the stored value.
func TestSyncContractorClearsMisprefixedNonEUNumber(t *testing.T) {
	var path, body string
	c, _ := newLocalCopyClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = io.WriteString(w, `{"status":{"code":"OK"},"contractors":{"0":{"contractor":{"id":"200295636"}}}}`)
	})
	stored := &Contractor{
		ID: "200295636", Name: "Buyer", Country: "HU", Zip: "1051", City: "Budapest",
		Street: "Street 1", Nip: "HUCHE266473703", TaxIdType: taxIdTypeCustom,
	}
	customer := &entity.ClientDetails{
		Name: "Buyer", Email: "buyer@example.com", Country: "HU", ZipCode: "1051",
		City: "Budapest", Street: "Street 1", TaxId: "CHE-266.473.703",
	}

	if err := c.syncContractor(context.Background(), stored, customer); err != nil {
		t.Fatalf("syncContractor: %v", err)
	}
	if !strings.Contains(path, "contractors/edit/200295636") {
		t.Fatalf("edit not called, last path %q", path)
	}
	var req struct {
		API struct {
			Contractors []struct {
				Contractor map[string]string `json:"contractor"`
			} `json:"contractors"`
		} `json:"api"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil || len(req.API.Contractors) != 1 {
		t.Fatalf("decode edit payload %q: %v", body, err)
	}
	got := req.API.Contractors[0].Contractor
	if got["nip"] != "" || got["tax_id_type"] != taxIdTypeNone {
		t.Errorf("nip/tax_id_type = %q/%q, want \"\"/%q", got["nip"], got["tax_id_type"], taxIdTypeNone)
	}
}
