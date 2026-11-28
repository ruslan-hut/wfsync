package entity

import (
	"math"
	"testing"

	"github.com/stripe/stripe-go/v76"
)

// ocLine mirrors how OpenCart stores a product row: a gross unit price with more than two
// decimals, rounded to whole cents per unit when it becomes a LineItem.
type ocLine struct {
	price float64
	qty   int64
}

func ocParams(lines []ocLine, shipping int64) *CheckoutParams {
	c := &CheckoutParams{}
	var total float64
	for _, l := range lines {
		total += l.price * float64(l.qty)
		c.LineItems = append(c.LineItems, &LineItem{Name: "p", Qty: l.qty, Price: int64(math.Round(l.price * 100))})
	}
	c.Total = int64(math.Round(total*100)) + shipping
	if shipping > 0 {
		c.AddShipping("", shipping)
	}
	return c
}

func assertFitted(t *testing.T, c *CheckoutParams, origQty map[int64]int64) {
	t.Helper()
	if got := c.ItemsTotal(); got != c.Total {
		t.Fatalf("items total %d, want %d", got, c.Total)
	}
	var units int64
	for _, item := range c.LineItems {
		if item.Price < 1 {
			t.Fatalf("non-positive price %d", item.Price)
		}
		if !item.Shipping {
			units += item.Qty
		}
	}
	var want int64
	for _, q := range origQty {
		want += q
	}
	if units != want {
		t.Fatalf("product units %d, want %d", units, want)
	}
}

func TestFitRoundingResidual(t *testing.T) {
	tests := []struct {
		name     string
		lines    []ocLine
		shipping int64
	}{
		{"order 18431: 2 × 39.8374", []ocLine{{39.8374, 2}}, 0},
		{"qty 2, .995", []ocLine{{12.995, 2}}, 0},
		{"qty 3, .335", []ocLine{{10.335, 3}}, 0},
		{"qty 3, .337", []ocLine{{10.337, 3}}, 0},
		{"qty 4, .125", []ocLine{{24.125, 4}}, 0},
		{"qty 4, .4449", []ocLine{{7.4449, 4}}, 0},
		{"qty 5, .005", []ocLine{{99.005, 5}}, 0},
		{"qty 5, .498", []ocLine{{3.498, 5}}, 0},
		{"qty 5, .502", []ocLine{{3.502, 5}}, 0},
		{"mixed lines with shipping", []ocLine{{19.995, 2}, {8.333, 3}, {41.0049, 4}, {5.555, 5}}, 1599},
		{"one unit lines only", []ocLine{{10.004, 1}, {20.006, 1}}, 0},
		{"already exact", []ocLine{{10.00, 3}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ocParams(tt.lines, tt.shipping)
			qty := map[int64]int64{}
			for i, l := range tt.lines {
				qty[int64(i)] = l.qty
			}
			if !c.FitRoundingResidual() {
				t.Fatalf("residual %d not fitted", c.Total-c.ItemsTotal())
			}
			assertFitted(t, c, qty)
			for _, item := range c.LineItems {
				if item.Shipping && item.Price != tt.shipping {
					t.Fatalf("shipping changed to %d", item.Price)
				}
			}
		})
	}
}

func TestFitRoundingResidualSplitsLastLine(t *testing.T) {
	c := ocParams([]ocLine{{39.8374, 2}}, 0)
	if c.Total != 7967 || c.ItemsTotal() != 7968 {
		t.Fatalf("setup: total %d, items %d", c.Total, c.ItemsTotal())
	}
	c.FitRoundingResidual()
	if len(c.LineItems) != 2 {
		t.Fatalf("lines %d, want 2", len(c.LineItems))
	}
	a, b := c.LineItems[0], c.LineItems[1]
	if a.Qty != 1 || a.Price != 3984 || b.Qty != 1 || b.Price != 3983 {
		t.Fatalf("got %d×%d + %d×%d", a.Qty, a.Price, b.Qty, b.Price)
	}
}

func TestFitRoundingResidualLeavesLargeGap(t *testing.T) {
	c := &CheckoutParams{Total: 1000, LineItems: []*LineItem{{Name: "p", Qty: 2, Price: 450}}}
	if c.FitRoundingResidual() {
		t.Fatal("a 100-cent gap on 2 units is not a rounding residual")
	}
	if c.LineItems[0].Price != 450 || len(c.LineItems) != 1 {
		t.Fatal("lines must stay untouched")
	}
}

func TestRecalcWithDiscountMatchesTotal(t *testing.T) {
	tests := []struct {
		name     string
		lines    []LineItem
		total    int64
		shipping int64
	}{
		// 28.11 payment handler: total 41978, lines 41980.
		{"2-cent gap on qty 3 and 4", []LineItem{{Qty: 3, Price: 7996}, {Qty: 4, Price: 4498}}, 41978, 0},
		{"1-cent gap on qty 2", []LineItem{{Qty: 2, Price: 3984}}, 7967, 0},
		{"coupon 10%", []LineItem{{Qty: 2, Price: 1999}, {Qty: 5, Price: 333}}, 5097, 0},
		{"coupon with shipping", []LineItem{{Qty: 3, Price: 1001}, {Qty: 4, Price: 250}}, 4199 + 1500, 1500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &CheckoutParams{Total: tt.total}
			for _, l := range tt.lines {
				l.Name = "p"
				c.LineItems = append(c.LineItems, &l)
			}
			if tt.shipping > 0 {
				c.AddShipping("", tt.shipping)
			}
			c.RecalcWithDiscount()
			if err := c.ValidateTotal(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNewFromCheckoutSessionKeepsTotal(t *testing.T) {
	sess := &stripe.CheckoutSession{
		AmountTotal: 3*3334 + 1,
		LineItems: &stripe.LineItemList{Data: []*stripe.LineItem{
			{Description: "p", Quantity: 3, AmountTotal: 10003},
		}},
	}
	p := NewFromCheckoutSession(sess)
	if err := p.ValidateTotal(); err != nil {
		t.Fatal(err)
	}
}

func TestB2BToCheckoutParamsKeepsTotal(t *testing.T) {
	tests := []struct {
		price float64
		qty   int64
	}{
		{12.345, 2}, {7.777, 3}, {0.995, 4}, {33.3333, 5},
	}
	for _, tt := range tests {
		o := &B2BOrder{
			OrderNumber: "1", CurrencyCode: "PLN", ClientCountry: "PL",
			Total: math.Round(tt.price*float64(tt.qty)*100) / 100,
			Items: []*B2BItem{{ProductName: "p", Quantity: tt.qty, Price: tt.price}},
		}
		p := o.ToCheckoutParams()
		if err := p.ValidateTotal(); err != nil {
			t.Fatalf("%v × %d: %v", tt.price, tt.qty, err)
		}
	}
}
