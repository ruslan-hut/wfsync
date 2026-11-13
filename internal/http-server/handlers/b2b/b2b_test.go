package b2b

import (
	"reflect"
	"testing"

	"wfsync/entity"
)

func TestBuildURLResponse(t *testing.T) {
	part1 := &entity.Payment{Id: "1", Link: "u1", Number: "PRO 1/11/2026", Date: "2026-11-13", Amount: 10000, Currency: "EUR"}
	part2 := &entity.Payment{Id: "2", Link: "u2", Number: "PRO 2/11/2026", Date: "2026-11-13", Amount: 550, Currency: "EUR"}
	head := *part1

	tests := []struct {
		name    string
		payment *entity.Payment
		want    urlResponse
	}{
		{
			name:    "single document",
			payment: part1,
			want: urlResponse{URL: "u1", URLs: []string{"u1"}, Documents: []documentResponse{
				{URL: "u1", Number: "PRO 1/11/2026", Date: "2026-11-13", Amount: 10000, Currency: "EUR"},
			}},
		},
		{
			name:    "split order lists every part",
			payment: &entity.Payment{Id: head.Id, Link: head.Link, Number: head.Number, Amount: head.Amount, Parts: []*entity.Payment{part1, nil, part2}},
			want: urlResponse{URL: "u1", URLs: []string{"u1", "u2"}, Documents: []documentResponse{
				{URL: "u1", Number: "PRO 1/11/2026", Date: "2026-11-13", Amount: 10000, Currency: "EUR"},
				{URL: "u2", Number: "PRO 2/11/2026", Date: "2026-11-13", Amount: 550, Currency: "EUR"},
			}},
		},
		{
			name:    "document recorded before numbers were kept",
			payment: &entity.Payment{Id: "3", Link: "u3", Amount: 700},
			want: urlResponse{URL: "u3", URLs: []string{"u3"}, Documents: []documentResponse{
				{URL: "u3", Amount: 700},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildURLResponse(tt.payment); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
