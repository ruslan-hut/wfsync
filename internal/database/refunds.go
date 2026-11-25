package database

import (
	"fmt"
	"time"
	"wfsync/entity"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnsureRefundIndexes makes refund_id the unique key of stripe_refunds, so concurrent
// writers (refund API, webhook, reconciler) can never store one refund twice. Idempotent.
func (m *MongoDB) EnsureRefundIndexes() error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionRefunds)
	_, err = collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "refund_id", Value: 1}},
			Options: options.Index().SetName("uniq_refund_id").SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "payment_id", Value: 1}},
			Options: options.Index().SetName("payment_id"),
		},
		{
			Keys:    bson.D{{Key: "idempotency_key", Value: 1}},
			Options: options.Index().SetName("idempotency_key").SetSparse(true),
		},
	})
	return err
}

// SaveRefund upserts a refund by refund_id and reports whether it was new. Only non-empty
// fields are written, so a webhook or reconciler copy (which knows nothing of the API
// caller) never clears requested_by / idempotency_key / reason set by the refund API,
// whichever of them lands first. source is overwritten only by the API.
func (m *MongoDB) SaveRefund(r *entity.Refund) (bool, error) {
	if r.RefundId == "" {
		return false, fmt.Errorf("empty refund id")
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return false, err
	}
	defer m.disconnect(ctx, connection)

	set := bson.D{{"amount", r.Amount}}
	for _, f := range []struct{ key, val string }{
		{"payment_id", r.PaymentId},
		{"session_id", r.SessionId},
		{"order_id", r.OrderId},
		{"currency", r.Currency},
		{"status", r.Status},
		{"reason", r.Reason},
		{"stripe_reason", r.StripeReason},
		{"requested_by", r.RequestedBy},
		{"idempotency_key", r.IdempotencyKey},
	} {
		if f.val != "" {
			set = append(set, bson.E{Key: f.key, Value: f.val})
		}
	}
	if !r.Created.IsZero() {
		set = append(set, bson.E{Key: "created", Value: r.Created})
	}
	onInsert := bson.D{{"recorded", time.Now()}}
	if r.Source == entity.RefundSourceAPI {
		set = append(set, bson.E{Key: "source", Value: r.Source})
	} else {
		onInsert = append(onInsert, bson.E{Key: "source", Value: entity.RefundSourceStripe})
	}

	collection := connection.Database(m.database).Collection(collectionRefunds)
	res, err := collection.UpdateOne(ctx,
		bson.D{{"refund_id", r.RefundId}},
		bson.D{{"$set", set}, {"$setOnInsert", onInsert}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return false, err
	}
	return res.UpsertedCount > 0, nil
}

// GetRefundsByPayment returns the refunds of a PaymentIntent, oldest first.
func (m *MongoDB) GetRefundsByPayment(paymentId string) ([]*entity.Refund, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionRefunds)
	cursor, err := collection.Find(ctx, bson.D{{"payment_id", paymentId}},
		options.Find().SetSort(bson.D{{"created", 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var result []*entity.Refund
	if err = cursor.All(ctx, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetRefundByIdempotencyKey returns the refund made with the given caller key, or nil.
func (m *MongoDB) GetRefundByIdempotencyKey(key string) (*entity.Refund, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionRefunds)
	var r entity.Refund
	if err = collection.FindOne(ctx, bson.D{{"idempotency_key", key}}).Decode(&r); err != nil {
		return nil, m.findError(err)
	}
	return &r, nil
}

// GetCheckoutParamsByPayment returns the most recently modified checkout params carrying
// the given PaymentIntent id, or nil.
func (m *MongoDB) GetCheckoutParamsByPayment(paymentId string) (*entity.CheckoutParams, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	opts := options.FindOne().SetSort(bson.D{{"modified", -1}})
	var params entity.CheckoutParams
	if err = collection.FindOne(ctx, bson.D{{"payment_id", paymentId}}, opts).Decode(&params); err != nil {
		return nil, m.findError(err)
	}
	return &params, nil
}
