package wfirma

type Invoice struct {
	Id          string                  `json:"id,omitempty" bson:"id"`
	Contractor  *Contractor             `json:"contractor" bson:"contractor"`
	Type        string                  `json:"type" bson:"type"`
	PriceType   string                  `json:"price_type" bson:"price_type"`
	Total       float64                 `json:"total" bson:"total"`
	IdExternal  string                  `json:"id_external" bson:"id_external"`
	Description string                  `json:"description" bson:"description"`
	Date        string                  `json:"date" bson:"date"`
	Currency    string                  `json:"currency" bson:"currency"`
	Contents    []*ContentLine          `json:"invoicecontents" bson:"invoicecontents"`
	Errors      map[string]ErrorWrapper `json:"errors,omitempty" bson:"errors,omitempty"`
}

type Content struct {
	Name  string  `json:"name" bson:"name"`
	Count int64   `json:"count" bson:"count"`
	Price float64 `json:"price" bson:"price"`
	Unit  string  `json:"unit" bson:"unit"`
	Vat   int     `json:"vat" bson:"vat"` // vat in percents: 0%, 23%
}

type ContentLine struct {
	Content *Content `json:"invoicecontent" bson:"invoicecontent"`
}
