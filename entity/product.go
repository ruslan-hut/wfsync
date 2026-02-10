package entity

type Product struct {
	Sku      string `json:"sku" bson:"sku" validate:"required"`
	WfirmaId int64  `json:"wfirma_id" bson:"wfirma_id" validate:"required"`
	Name     string `json:"name" bson:"name"`
}
