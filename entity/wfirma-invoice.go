package entity

// LocalInvoice represents a stored wFirma invoice document.
// Mirrors the wfirma.Invoice BSON structure to avoid import cycles between
// the database and wfirma packages. Used for sync operations that need to
// read invoices back from MongoDB and potentially re-create them on wFirma.
type LocalInvoice struct {
	Id            string              `json:"id" bson:"id"`
	Number        string              `json:"number,omitempty" bson:"number,omitempty"`
	Contractor    *LocalContractor    `json:"contractor" bson:"contractor"`
	Type          string              `json:"type" bson:"type"`
	PriceType     string              `json:"price_type" bson:"price_type"`
	PaymentMethod string              `json:"paymentmethod" bson:"paymentmethod"`
	PaymentDate   string              `json:"paymentdate" bson:"paymentdate"`
	DisposalDate  string              `json:"disposaldate" bson:"disposaldate"`
	Total         float64             `json:"total" bson:"total"`
	IdExternal    string              `json:"id_external" bson:"id_external"`
	Description   string              `json:"description" bson:"description"`
	Date          string              `json:"date" bson:"date"`
	Currency      string              `json:"currency" bson:"currency"`
	Contents      []*LocalContentLine `json:"invoicecontents" bson:"invoicecontents"`
}

// LocalContractor mirrors wfirma.Contractor for local storage.
type LocalContractor struct {
	ID      string `json:"id" bson:"id"`
	City    string `json:"city,omitempty" bson:"city,omitempty"`
	Country string `json:"country,omitempty" bson:"country,omitempty"`
	Email   string `json:"email,omitempty" bson:"email,omitempty"`
	Name    string `json:"name,omitempty" bson:"name,omitempty"`
	Zip     string `json:"zip,omitempty" bson:"zip,omitempty"`
}

// LocalContentLine mirrors wfirma.ContentLine for local storage.
type LocalContentLine struct {
	Content *LocalContent `json:"invoicecontent" bson:"invoicecontent"`
}

// LocalContent mirrors wfirma.Content for local storage.
type LocalContent struct {
	Name    string           `json:"name" bson:"name"`
	Good    *LocalGoodRef    `json:"good,omitempty" bson:"good,omitempty"`
	Count   int64            `json:"count" bson:"count"`
	Price   float64          `json:"price" bson:"price"`
	Unit    string           `json:"unit" bson:"unit"`
	Vat     string           `json:"vat,omitempty" bson:"vat"`
	VatCode *LocalVatCodeRef `json:"vat_code,omitempty" bson:"vat_code,omitempty"`
}

// LocalGoodRef mirrors wfirma.GoodRef for local storage.
type LocalGoodRef struct {
	ID int64 `json:"id" bson:"id"`
}

// LocalVatCodeRef mirrors wfirma.VatCodeRef for local storage.
type LocalVatCodeRef struct {
	ID int64 `json:"id" bson:"id"`
}

// SyncResult contains counters returned by the sync endpoints.
type SyncResult struct {
	RemoteCount int `json:"remote_count"`
	LocalCount  int `json:"local_count"`
	Upserted    int `json:"upserted"`
	Deleted     int `json:"deleted"`
	Recreated   int `json:"recreated"`
}
