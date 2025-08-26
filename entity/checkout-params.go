package entity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	"wfsync/lib/validate"

	"github.com/biter777/countries"
	"github.com/stripe/stripe-go/v76"
)

type Source string

const (
	SourceApi      Source = "api"
	SourceStripe   Source = "stripe"
	SourceOpenCart Source = "opencart"
)

type CheckoutParams struct {
	ClientDetails *ClientDetails `json:"client_details" bson:"client_details" validate:"required"`
	LineItems     []*LineItem    `json:"line_items" bson:"line_items" validate:"required,min=1,dive"`
	Total         int64          `json:"total" bson:"total" validate:"required,min=1"`
	Shipping      int64          `json:"shipping,omitempty" bson:"shipping,omitempty"`
	Currency      string         `json:"currency" bson:"currency" validate:"required,oneof=PLN EUR"`
	CurrencyValue float64        `json:"currency_value,omitempty" bson:"currency_value,omitempty"`
	OrderId       string         `json:"order_id" bson:"order_id" validate:"required,min=1,max=32"`
	SuccessUrl    string         `json:"success_url" bson:"success_url" validate:"required,url"`
	Created       time.Time      `json:"created" bson:"created"`
	Closed        time.Time      `json:"closed,omitempty" bson:"closed"`
	Status        string         `json:"status" bson:"status"`
	SessionId     string         `json:"session_id,omitempty" bson:"session_id,omitempty"`
	InvoiceId     string         `json:"invoice_id,omitempty" bson:"invoice_id,omitempty"`
	InvoiceFile   string         `json:"invoice_file,omitempty" bson:"invoice_file,omitempty"`
	ProformaId    string         `json:"proforma_id,omitempty" bson:"proforma_id,omitempty"`
	ProformaFile  string         `json:"proforma_file,omitempty" bson:"proforma_file,omitempty"`
	Paid          bool           `json:"paid,omitempty" bson:"paid"`
	Source        Source         `json:"source,omitempty" bson:"source"`
	Payload       interface{}    `json:"payload,omitempty" bson:"payload,omitempty"`
}

func (c *CheckoutParams) Bind(_ *http.Request) error {
	c.Created = time.Now()
	return validate.Struct(c)
}

func (c *CheckoutParams) ItemsTotal() int64 {
	var total int64
	for _, item := range c.LineItems {
		total += item.Qty * item.Price
	}
	return total
}

func (c *CheckoutParams) ValidateTotal() error {
	total := c.ItemsTotal()
	if c.Total == total {
		return nil
	}
	return fmt.Errorf("total amount %d does not match sum of line items %d", c.Total, total)
}

func (c *CheckoutParams) Validate() error {
	if len(c.LineItems) == 0 {
		return fmt.Errorf("no line items")
	}
	if c.ClientDetails == nil {
		return fmt.Errorf("no client details")
	}
	//err := c.ValidateTotal()
	//if err != nil {
	//	return err
	//}
	return nil
}

func (c *CheckoutParams) RefineTotal(_ int) error {
	// Цель: привести сумму по строкам (не включая shipping) к (c.Total - shippingTotal),
	// равномерно распределив разницу по строкам (без изменения shipping).
	// Коррекция цен выполняется на уровне цены за единицу (Price), что меняет итог на Qty строки.

	if len(c.LineItems) == 0 {
		return nil
	}

	// Суммируем shipping из строк (на случай расхождений с c.Shipping)
	var shippingTotal int64
	var nonShipIdxs []int
	var nonShipQtySum int64
	var nonShipTotal int64

	for i, it := range c.LineItems {
		if it.Shipping {
			shippingTotal += it.Price * it.Qty
			continue
		}
		nonShipIdxs = append(nonShipIdxs, i)
		nonShipQtySum += it.Qty
		nonShipTotal += it.Price * it.Qty
	}

	// Если нечего корректировать (все строки shipping или их нет)
	if len(nonShipIdxs) == 0 || nonShipQtySum == 0 {
		// В этой ситуации мы не имеем права трогать shipping, поэтому либо уже всё совпадает, либо сообщаем об ошибке.
		targetNonShip := c.Total - shippingTotal
		if targetNonShip != nonShipTotal {
			return fmt.Errorf("cannot refine: no non-shipping items to adjust")
		}
		return nil
	}

	targetNonShip := c.Total - shippingTotal
	diff := targetNonShip - nonShipTotal
	if diff == 0 {
		return nil
	}

	// Базовая равномерная поправка по цене единицы: base = diff / sumQtyNonShip
	base := diff / nonShipQtySum // может быть 0, это нормально
	remainder := diff % nonShipQtySum

	// Применяем базовую поправку ко всем не-shipping строкам с защитой от цены < 1
	// Если снижаем меньше 1 — недоприменённую часть возвращаем в remainder (как "не получилось снять").
	for _, idx := range nonShipIdxs {
		item := c.LineItems[idx]
		if base == 0 {
			continue
		}
		newPrice := item.Price + base
		if newPrice < 1 {
			// Сколько единичных шагов мы реально можем снять/прибавить, не опуская ниже 1
			applied := int64(1) - item.Price
			// applied <= base (отрицательная величина по модулю меньше)
			item.Price = 1
			// Возвращаем неприменённое в remainder как Qty * (base - applied)
			// знак сохраняем: если base < 0, то (base - applied) <= 0
			// Нам нужно добавить назад к remainder "не снятую" часть
			unapplied := base - applied
			remainder += unapplied * item.Qty
		} else {
			item.Price = newPrice
			// базовая часть учтена полностью
		}
	}

	// После базовой корректировки перерасчитывать ItemsTotal не обязательно — мы ведём остаток в remainder.

	if remainder == 0 {
		return nil
	}

	// Остаток раздаём по строкам шагами +/-1 к Price,
	// где один шаг меняет итог на Qty строки.
	// Чтобы минимизировать "перескок", начинаем с меньших Qty.
	type idxQty struct {
		idx int
		q   int64
	}
	var order []idxQty
	for _, idx := range nonShipIdxs {
		order = append(order, idxQty{idx: idx, q: c.LineItems[idx].Qty})
	}
	// Сортировка по возрастанию Qty
	for i := 0; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			if order[j].q < order[i].q {
				order[i], order[j] = order[j], order[i]
			}
		}
	}

	// Жадно пытаемся погасить remainder.
	// Лимит итераций: не более 2 полных проходов по списку.
	passes := 0
	for remainder != 0 && passes < 2 {
		progress := false
		for _, iq := range order {
			if remainder == 0 {
				break
			}
			item := c.LineItems[iq.idx]
			q := iq.q

			// Сколько шагов можно применить по знаку, не переходя через 1.
			if remainder > 0 {
				// Нужен +1 к цене, что даст +Qty к остатку
				steps := remainder / q // max сколько раз можно применить без избыточного шага
				if steps == 0 {
					// Если даже один шаг даст перебор, пропускаем к следующей строке
					continue
				}
				item.Price += steps
				remainder -= steps * q
				progress = true
			} else { // remainder < 0
				// Нужен -1 к цене (минус Qty к остатку), но не ниже 1
				maxDec := item.Price - 1 // максимальное количество "минус 1" по цене
				if maxDec <= 0 {
					continue
				}
				// Сколько шагов по Qty нам нужно
				needed := (-remainder) / q
				if needed == 0 {
					continue
				}
				if needed > maxDec {
					needed = maxDec
				}
				item.Price -= needed
				remainder += needed * q
				progress = true
			}
		}
		if !progress {
			// Попытка сделать одиночные шаги, даже если они "перешагнут" (когда remainder < min Qty по модулю)
			for _, iq := range order {
				if remainder == 0 {
					break
				}
				item := c.LineItems[iq.idx]
				q := iq.q
				if remainder > 0 {
					// Один шаг +1 по цене
					item.Price += 1
					remainder -= q
					progress = true
				} else {
					// Один шаг -1 по цене, проверим границу
					if item.Price > 1 {
						item.Price -= 1
						remainder += q
						progress = true
					}
				}
			}
		}
		passes++
	}

	if remainder != 0 {
		// Точную раздачу сделать невозможно из-за ограничений кратности Qty и min price.
		return fmt.Errorf("cannot refine exactly due to quantities granularity; remainder=%d", remainder)
	}

	return nil
}

func (c *CheckoutParams) AddShipping(title string, amount int64) {
	c.Shipping = amount
	c.LineItems = append(c.LineItems, ShippingLineItem(title, amount))
}

func (c *CheckoutParams) RecalcWithDiscount() {
	// Цель: привести сумму по НЕ-shipping строкам к (c.Total - сумма shipping-строк),
	// распределив скидку (или наценку) построчно по товарам, не затрагивая shipping.
	if len(c.LineItems) == 0 {
		return
	}

	// Суммируем shipping по строкам и собираем индексы товарных строк
	var shippingTotal int64
	var nonShipIdxs []int
	var nonShipQtySum int64
	var nonShipTotal int64

	for i, it := range c.LineItems {
		if it.Shipping {
			shippingTotal += it.Price * it.Qty
			continue
		}
		nonShipIdxs = append(nonShipIdxs, i)
		nonShipQtySum += it.Qty
		nonShipTotal += it.Price * it.Qty
	}

	// Если корректировать нечего (нет товарных строк), просто выходим
	if len(nonShipIdxs) == 0 || nonShipQtySum == 0 {
		return
	}

	targetNonShip := c.Total - shippingTotal
	diff := targetNonShip - nonShipTotal
	if diff == 0 {
		return
	}

	// Базовое равномерное изменение unit-price для всех товарных строк.
	// Коррекция выполняется на уровне цены за единицу (Price), что меняет итог на Qty строки.
	base := diff / nonShipQtySum // может быть 0 — тогда всё уйдёт в remainder
	remainder := diff % nonShipQtySum

	for _, idx := range nonShipIdxs {
		item := c.LineItems[idx]
		if base == 0 {
			continue
		}
		newPrice := item.Price + base
		if newPrice < 1 {
			// Сколько можем реально применить, не опускаясь ниже 1
			applied := int64(1) - item.Price
			item.Price = 1
			// Неиспользованную часть возвращаем в остаток (с учётом количества)
			unapplied := base - applied
			remainder += unapplied * item.Qty
		} else {
			item.Price = newPrice
		}
	}

	if remainder == 0 {
		return
	}

	// Распределяем остаток маленькими шагами по строкам.
	// Начинаем с меньших Qty, чтобы уменьшить "квантование" итога.
	type idxQty struct {
		idx int
		q   int64
	}
	var order []idxQty
	for _, idx := range nonShipIdxs {
		order = append(order, idxQty{idx: idx, q: c.LineItems[idx].Qty})
	}
	// Простая сортировка по возрастанию Qty (без импортов)
	for i := 0; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			if order[j].q < order[i].q {
				order[i], order[j] = order[j], order[i]
			}
		}
	}

	// До двух проходов: сначала крупными шагами, затем одиночными.
	passes := 0
	for remainder != 0 && passes < 2 {
		progress := false
		for _, iq := range order {
			if remainder == 0 {
				break
			}
			item := c.LineItems[iq.idx]
			q := iq.q

			if remainder > 0 {
				// Нужны увеличения цены (+1 price даёт +Qty к итогу)
				steps := remainder / q
				if steps == 0 {
					continue
				}
				item.Price += steps
				remainder -= steps * q
				progress = true
			} else { // remainder < 0
				// Нужны уменьшения цены (-1 price даёт -Qty к итогу), но не ниже 1
				maxDec := item.Price - 1
				if maxDec <= 0 {
					continue
				}
				needed := (-remainder) / q
				if needed == 0 {
					continue
				}
				if needed > maxDec {
					needed = maxDec
				}
				item.Price -= needed
				remainder += needed * q
				progress = true
			}
		}
		if !progress {
			// Одиночные шаги, даже если дадут "перешагивание" минимального кванта
			for _, iq := range order {
				if remainder == 0 {
					break
				}
				item := c.LineItems[iq.idx]
				q := iq.q

				if remainder > 0 {
					item.Price += 1
					remainder -= q
				} else {
					if item.Price > 1 {
						item.Price -= 1
						remainder += q
					}
				}
			}
		}
		passes++
	}

	// Если remainder != 0, значит точное совпадение недостижимо при данных Qty и ограничении price>=1.
	// В этой ситуации сделали максимально возможное приближение, не трогая shipping.
}

type LineItem struct {
	Name     string `json:"name" validate:"required"`
	Qty      int64  `json:"qty" validate:"required,min=1"`
	Price    int64  `json:"price" validate:"required,min=1"`
	Sku      string `json:"sku,omitempty" bson:"sku"`
	Shipping bool   `json:"shipping,omitempty" bson:"shipping"`
}

func ShippingLineItem(title string, amount int64) *LineItem {
	if title == "" {
		title = "Zwrot kosztów transportu towarów"
	} else {
		title = fmt.Sprintf("Zwrot kosztów transportu towarów (%s)", title)
	}
	return &LineItem{
		Name:     title,
		Qty:      1,
		Price:    amount,
		Shipping: true,
	}
}

type ClientDetails struct {
	Name    string `json:"name" bson:"name" validate:"required"`
	Email   string `json:"email" bson:"email" validate:"required,email"`
	Phone   string `json:"phone" bson:"phone"`
	Country string `json:"country" bson:"country"`
	ZipCode string `json:"zip_code" bson:"zip_code"`
	City    string `json:"city" bson:"city"`
	Street  string `json:"street" bson:"street"`
	TaxId   string `json:"tax_id" bson:"tax_id"`
}

func (c *ClientDetails) CountryCode() string {
	if c.Country == "" {
		return ""
	}
	if len(c.Country) == 2 {
		return c.Country
	}
	country := countries.ByName(c.Country)
	code := country.Alpha2()
	if len(code) == 2 {
		return code
	}
	return ""
}

// ParseTaxId extracts a tax ID from a JSON-formatted string based on the given field ID and assigns it to the ClientDetails.
// Returns an error if the provided raw data is invalid JSON or the extraction fails.
// Raw string example: {"2":"DE362155758"}
func (c *ClientDetails) ParseTaxId(fieldId, raw string) error {
	if fieldId == "" || raw == "" {
		return nil
	}
	//var jsonStr string
	//if err := json.Unmarshal([]byte(raw), &jsonStr); err != nil {
	//	return err
	//}
	var data map[string]string
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return err
	}
	c.TaxId = data[fieldId]
	return nil
}

func NewFromCheckoutSession(sess *stripe.CheckoutSession) *CheckoutParams {
	params := &CheckoutParams{
		SessionId: sess.ID,
		Status:    string(sess.Status),
		Created:   time.Now(),
		Currency:  string(sess.Currency),
		Total:     sess.AmountTotal,
		Paid:      sess.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid,
		Payload:   sess,
		Source:    SourceStripe,
	}
	if sess.Customer != nil {
		client := &ClientDetails{
			Name:  sess.Customer.Name,
			Email: sess.Customer.Email,
			Phone: sess.Customer.Phone,
		}
		if sess.Customer.Address != nil {
			client.Country = sess.Customer.Address.Country
			client.ZipCode = sess.Customer.Address.PostalCode
			client.City = sess.Customer.Address.City
			client.Street = fmt.Sprintf("%s %s", sess.Customer.Address.Line1, sess.Customer.Address.Line2)
		}
		params.ClientDetails = client
	}
	if sess.LineItems != nil {
		for _, item := range sess.LineItems.Data {
			if item.Quantity == 0 {
				continue
			}
			lineItem := &LineItem{
				Name:  item.Description,
				Qty:   item.Quantity,
				Price: item.AmountTotal / item.Quantity,
			}
			params.LineItems = append(params.LineItems, lineItem)
		}
	}
	if sess.ShippingCost != nil && sess.ShippingCost.AmountTotal > 0 {
		params.AddShipping("", sess.ShippingCost.AmountTotal)
	}
	if sess.Metadata != nil {
		id, ok := sess.Metadata["order_id"]
		if ok {
			params.OrderId = id
		}
	}
	if params.OrderId == "" {
		params.OrderId = sess.ID
	}
	return params
}

func NewFromInvoice(inv *stripe.Invoice) *CheckoutParams {
	params := &CheckoutParams{
		SessionId: inv.ID,
		Status:    string(inv.Status),
		Created:   time.Now(),
		Currency:  string(inv.Currency),
		Total:     inv.Total,
		Paid:      inv.Paid,
		Payload:   inv,
		Source:    SourceStripe,
	}
	if inv.Customer != nil {
		client := &ClientDetails{
			Name:  inv.Customer.Name,
			Email: inv.Customer.Email,
			Phone: inv.Customer.Phone,
		}
		if inv.Customer.Address != nil {
			client.Country = inv.Customer.Address.Country
			client.ZipCode = inv.Customer.Address.PostalCode
			client.City = inv.Customer.Address.City
			client.Street = fmt.Sprintf("%s %s", inv.Customer.Address.Line1, inv.Customer.Address.Line2)
		}
		params.ClientDetails = client
	}
	if inv.Lines != nil {
		for _, item := range inv.Lines.Data {
			if item.Quantity == 0 {
				continue
			}
			lineItem := &LineItem{
				Name:  item.Description,
				Qty:   item.Quantity,
				Price: item.Amount / item.Quantity,
			}
			params.LineItems = append(params.LineItems, lineItem)
		}
	}
	if inv.ShippingCost != nil && inv.ShippingCost.AmountTotal > 0 {
		params.AddShipping("", inv.ShippingCost.AmountTotal)
	}
	if inv.Metadata != nil {
		id, ok := inv.Metadata["order_id"]
		if ok {
			params.OrderId = id
		}
	}
	if params.OrderId == "" {
		params.OrderId = inv.ID
	}
	return params
}
