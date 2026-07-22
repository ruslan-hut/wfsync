package database

import (
	"context"
	"errors"
	"fmt"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	collectionUsers           = "users"
	collectionCheckoutParams  = "checkout_params"
	collectionInvoice         = "wfirma_invoice"
	collectionProducts        = "products"
	collectionInviteCodes     = "invite_codes"
	collectionVATRates        = "vat_rates"
	collectionVIESValidations = "vies_validations"
	collectionRetryJobs       = "retry_jobs"
	collectionBankAccounts    = "wfirma_bank_accounts"
)

type MongoDB struct {
	clientOptions *options.ClientOptions
	database      string
}

// opTimeout bounds the duration of a single MongoDB operation. Avoids
// indefinite blocking when the DB stalls or the network is degraded.
const opTimeout = 30 * time.Second

// opCtx returns a fresh per-operation context with timeout. Each public method
// uses this since request-scoped context is not propagated through the current
// interface; a per-op deadline is a strict improvement over the prior
// long-lived context.Background().
func (m *MongoDB) opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}

func NewMongoClient(conf *config.Config) *MongoDB {
	if !conf.Mongo.Enabled {
		return nil
	}
	connectionUri := fmt.Sprintf("mongodb://%s:%s", conf.Mongo.Host, conf.Mongo.Port)
	clientOptions := options.Client().ApplyURI(connectionUri)
	if conf.Mongo.User != "" {
		clientOptions.SetAuth(options.Credential{
			Username:   conf.Mongo.User,
			Password:   conf.Mongo.Password,
			AuthSource: conf.Mongo.Database,
		})
	}
	client := &MongoDB{
		clientOptions: clientOptions,
		database:      conf.Mongo.Database,
	}
	return client
}

func (m *MongoDB) connect(ctx context.Context) (*mongo.Client, error) {
	connection, err := mongo.Connect(ctx, m.clientOptions)
	if err != nil {
		return nil, fmt.Errorf("mongodb connect: %w", err)
	}
	return connection, nil
}

func (m *MongoDB) disconnect(ctx context.Context, connection *mongo.Client) {
	_ = connection.Disconnect(ctx)
}

// Close is a no-op since connections are created per-operation.
// This method exists for interface consistency with other databases.
func (m *MongoDB) Close(_ context.Context) error {
	return nil
}

func (m *MongoDB) findError(err error) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	return fmt.Errorf("mongodb find: %w", err)
}

func (m *MongoDB) Save(key string, value interface{}) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(key)
	_, err = collection.InsertOne(ctx, value)
	return err
}

func (m *MongoDB) GetUser(token string) (*entity.User, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"token", token}}
	var user entity.User
	if err = collection.FindOne(ctx, filter).Decode(&user); err != nil {
		return nil, m.findError(err)
	}
	return &user, nil
}

func (m *MongoDB) GetTelegramUsers() ([]*entity.User, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_id", bson.D{{"$gt", 0}}}, {"telegram_enabled", true}}
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	var users []*entity.User
	err = cursor.All(ctx, &users)
	if err != nil {
		return nil, err
	}
	return users, nil
}

func (m *MongoDB) SetTelegramEnabled(id int64, isActive bool, logLevel int) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)
	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_id", id}}
	update := bson.D{{"$set", bson.D{
		{"telegram_enabled", isActive},
		{"log_level", logLevel},
	}}}
	_, err = collection.UpdateOne(ctx, filter, update)
	return err
}

func (m *MongoDB) SaveCheckoutParams(params *entity.CheckoutParams) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	now := time.Now()
	if params.Created.IsZero() {
		params.Created = now
	}
	params.Modified = now

	collection := connection.Database(m.database).Collection(collectionCheckoutParams)

	// order_id is the canonical identity: one document per OpenCart order, so every write
	// path (Stripe hold/webhook/capture and the wFirma-only invoice flows) converges on the
	// same record instead of inserting a fresh one. session_id/event_id remain as fallbacks
	// only for the rare record that carries no order_id. The omitempty bson tags on the
	// linkage ids (session_id, payment_id, event_id, invoice_id, proforma_id) mean an upsert
	// never clears an id it does not carry — so a wFirma re-invoice cannot wipe the Stripe
	// references already stored on the order.
	var filter bson.D
	switch {
	case params.OrderId != "":
		filter = bson.D{{"order_id", params.OrderId}}
	case params.SessionId != "":
		filter = bson.D{{"session_id", params.SessionId}}
	case params.EventId != "":
		filter = bson.D{{"event_id", params.EventId}}
	default:
		_, err = collection.InsertOne(ctx, params)
		return err
	}

	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, bson.D{{"$set", params}}, opts)
	return err
}

func (m *MongoDB) UpdateCheckoutParams(params *entity.CheckoutParams) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	filter := bson.D{{"order_id", params.OrderId}}
	update := bson.D{{"$set", bson.D{
		{"invoice_id", params.InvoiceId},
		{"proforma_id", params.ProformaId},
		{"closed", time.Now()},
	}}}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	return err
}

// CloseCheckoutParams marks a checkout params document resolved by stamping closed
// (and invoice_id when provided), keyed on payment_id so it always targets the original
// document even if the in-memory order_id was repaired. payment_id is used rather than
// session_id because the reconciler only ever processes records that already carry a
// PaymentIntent, while session_id can be empty (e.g. foreign or legacy records) — keying
// on an empty field would silently fail to close the record and re-surface it every tick.
// Never upserts: a missing match is a no-op rather than a phantom insert.
func (m *MongoDB) CloseCheckoutParams(paymentId, invoiceId string) error {
	if paymentId == "" {
		return fmt.Errorf("empty payment id")
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	set := bson.D{{"closed", time.Now()}}
	if invoiceId != "" {
		set = append(set, bson.E{Key: "invoice_id", Value: invoiceId})
	}
	_, err = collection.UpdateMany(ctx, bson.D{{"payment_id", paymentId}}, bson.D{{"$set", set}})
	return err
}

func (m *MongoDB) GetCheckoutParamsForEvent(eventId string) (*entity.CheckoutParams, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)
	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	filter := bson.D{{"event_id", eventId}}
	var params entity.CheckoutParams
	err = collection.FindOne(ctx, filter).Decode(&params)
	if err != nil {
		return nil, m.findError(err)
	}
	return &params, nil
}

func (m *MongoDB) GetCheckoutParamsSession(sessionId string) (*entity.CheckoutParams, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)
	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	filter := bson.D{{"session_id", sessionId}}
	var params entity.CheckoutParams
	err = collection.FindOne(ctx, filter).Decode(&params)
	if err != nil {
		return nil, m.findError(err)
	}
	return &params, nil
}

// GetStripeOrderIds returns a set of order IDs that have a non-empty session_id
// in the checkout_params collection. Used to determine which orders were paid via Stripe.
// reconcileClosedSentinel is an early date used to tell an unset Created/Closed
// timestamp (Go zero value 0001-01-01) apart from a real one. Records closed after a
// real action have Closed = time.Now(), which is always after this sentinel.
var reconcileClosedSentinel = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// GetUnresolvedHeldParams returns checkout params that have a PaymentIntent but no
// invoice yet and have not been closed by a prior reconciliation. These are the holds
// the reconciler must inspect against live Stripe state. Sessions that never produced
// a PaymentIntent (abandoned before authorization) are excluded — they never held
// funds and need no action.
func (m *MongoDB) GetUnresolvedHeldParams(limit int) ([]*entity.CheckoutParams, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	filter := bson.D{
		{"payment_id", bson.D{{"$nin", bson.A{"", nil}}}},
		{"$or", bson.A{
			bson.D{{"invoice_id", ""}},
			bson.D{{"invoice_id", bson.D{{"$exists", false}}}},
		}},
		{"$or", bson.A{
			bson.D{{"closed", bson.D{{"$exists", false}}}},
			bson.D{{"closed", bson.D{{"$lt", reconcileClosedSentinel}}}},
		}},
	}
	opts := options.Find().SetSort(bson.D{{"created", 1}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}

	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var result []*entity.CheckoutParams
	if err = cursor.All(ctx, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetCheckoutParamsByOrder returns the most recently modified checkout params for an
// order. An order may have several documents (e.g. a re-issued hold), so we sort by
// modified descending and return the latest.
func (m *MongoDB) GetCheckoutParamsByOrder(orderId string) (*entity.CheckoutParams, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)
	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	filter := bson.D{{"order_id", orderId}}
	opts := options.FindOne().SetSort(bson.D{{"modified", -1}})
	var params entity.CheckoutParams
	err = collection.FindOne(ctx, filter, opts).Decode(&params)
	if err != nil {
		return nil, m.findError(err)
	}
	return &params, nil
}

func (m *MongoDB) GetStripeOrderIds(orderIds []string) (map[string]bool, error) {
	if len(orderIds) == 0 {
		return nil, nil
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	filter := bson.D{
		{"order_id", bson.D{{"$in", orderIds}}},
		{"session_id", bson.D{{"$ne", ""}}},
	}

	cursor, err := collection.Find(ctx, filter, options.Find().SetProjection(bson.D{{"order_id", 1}}))
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	result := make(map[string]bool)
	for cursor.Next(ctx) {
		var doc struct {
			OrderId string `bson:"order_id"`
		}
		if err = cursor.Decode(&doc); err != nil {
			return nil, err
		}
		if doc.OrderId != "" {
			result[doc.OrderId] = true
		}
	}
	return result, cursor.Err()
}

func (m *MongoDB) GetProductBySku(sku string) (*entity.Product, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionProducts)
	filter := bson.D{{"sku", sku}}
	var product entity.Product
	err = collection.FindOne(ctx, filter).Decode(&product)
	if err != nil {
		return nil, m.findError(err)
	}
	return &product, nil
}

func (m *MongoDB) SaveProduct(product *entity.Product) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionProducts)
	filter := bson.D{{"sku", product.Sku}}
	update := bson.D{{"$set", product}}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	return err
}

func (m *MongoDB) SaveInvoice(id string, invoice interface{}) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionInvoice)
	filter := bson.D{{"id", id}}
	update := bson.D{{"$set", invoice}}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	return err
}

// GetInvoicesByDateRange returns locally stored invoices matching a date range and type.
// String comparison on YYYY-MM-DD formatted dates works correctly for range filtering.
func (m *MongoDB) GetInvoicesByDateRange(from, to, invType string) ([]*entity.LocalInvoice, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionInvoice)
	filter := bson.D{
		{"date", bson.D{{"$gte", from}}},
		{"date", bson.D{{"$lte", to}}},
		{"type", invType},
	}
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	var invoices []*entity.LocalInvoice
	err = cursor.All(ctx, &invoices)
	if err != nil {
		return nil, err
	}
	return invoices, nil
}

// DeleteInvoiceById removes a single invoice document by its wFirma ID.
func (m *MongoDB) DeleteInvoiceById(id string) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionInvoice)
	filter := bson.D{{"id", id}}
	_, err = collection.DeleteOne(ctx, filter)
	return err
}

// UpdateInvoiceNumber sets the invoice number for an existing invoice document.
func (m *MongoDB) UpdateInvoiceNumber(id, number string) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionInvoice)
	filter := bson.D{{"id", id}}
	update := bson.D{{"$set", bson.D{{"number", number}}}}
	_, err = collection.UpdateOne(ctx, filter, update)
	return err
}

// GetAllTelegramUsers returns all users with telegram_id > 0 (includes pending/disabled).
func (m *MongoDB) GetAllTelegramUsers() ([]*entity.User, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_id", bson.D{{"$gt", 0}}}}
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	var users []*entity.User
	err = cursor.All(ctx, &users)
	if err != nil {
		return nil, err
	}
	return users, nil
}

// GetTelegramUserById returns a single user by telegram ID.
func (m *MongoDB) GetTelegramUserById(telegramId int64) (*entity.User, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_id", telegramId}}
	var user entity.User
	err = collection.FindOne(ctx, filter).Decode(&user)
	if err != nil {
		return nil, m.findError(err)
	}
	return &user, nil
}

// RegisterTelegramUser upserts a new user with role=pending.
func (m *MongoDB) RegisterTelegramUser(telegramId int64, username string) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_id", telegramId}}
	update := bson.D{
		{"$setOnInsert", bson.D{
			{"telegram_id", telegramId},
			{"telegram_role", entity.RolePending},
			{"telegram_enabled", false},
			{"subscription_tier", entity.TierRealtime},
			{"registered_at", time.Now()},
			{"username", username},
			{"token", ""},
		}},
		{"$set", bson.D{
			{"telegram_username", username},
		}},
	}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	return err
}

// SetTelegramRole sets the telegram role for a user.
func (m *MongoDB) SetTelegramRole(telegramId int64, role entity.TelegramRole) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_id", telegramId}}
	update := bson.D{{"$set", bson.D{
		{"telegram_role", role},
		{"telegram_enabled", role == entity.RoleUser || role == entity.RoleAdmin},
	}}}
	_, err = collection.UpdateOne(ctx, filter, update)
	return err
}

// GetPendingTelegramUsers returns users with role=pending.
func (m *MongoDB) GetPendingTelegramUsers() ([]*entity.User, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_role", entity.RolePending}}
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	var users []*entity.User
	err = cursor.All(ctx, &users)
	if err != nil {
		return nil, err
	}
	return users, nil
}

// SetTelegramTopics sets the topic subscriptions for a user.
func (m *MongoDB) SetTelegramTopics(telegramId int64, topics []string) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_id", telegramId}}
	update := bson.D{{"$set", bson.D{{"telegram_topics", topics}}}}
	_, err = collection.UpdateOne(ctx, filter, update)
	return err
}

// SetSubscriptionTier sets the subscription tier and digest schedule for a user.
func (m *MongoDB) SetSubscriptionTier(telegramId int64, tier entity.SubscriptionTier, schedule string) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_id", telegramId}}
	update := bson.D{{"$set", bson.D{
		{"subscription_tier", tier},
		{"digest_schedule", schedule},
	}}}
	_, err = collection.UpdateOne(ctx, filter, update)
	return err
}

// CreateInviteCode stores a new invite code.
func (m *MongoDB) CreateInviteCode(code *entity.InviteCode) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionInviteCodes)
	_, err = collection.InsertOne(ctx, code)
	return err
}

// UseInviteCode atomically finds and uses an invite code.
func (m *MongoDB) UseInviteCode(code string, telegramId int64) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionInviteCodes)
	filter := bson.D{
		{"code", code},
		{"$expr", bson.D{{"$lt", bson.A{"$use_count", "$max_uses"}}}},
	}
	update := bson.D{
		{"$set", bson.D{
			{"used_by", telegramId},
			{"used_at", time.Now()},
		}},
		{"$inc", bson.D{{"use_count", 1}}},
	}
	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("invite code not found or exhausted")
	}
	return nil
}

// SaveVATRate upserts a VAT rate document by country_code.
func (m *MongoDB) SaveVATRate(rate *entity.VATRate) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionVATRates)
	filter := bson.D{{"country_code", rate.CountryCode}}
	update := bson.D{{"$set", rate}}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	return err
}

// GetAllVATRates returns all VAT rate documents from the collection.
func (m *MongoDB) GetAllVATRates() ([]*entity.VATRate, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionVATRates)
	cursor, err := collection.Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	var rates []*entity.VATRate
	err = cursor.All(ctx, &rates)
	if err != nil {
		return nil, err
	}
	return rates, nil
}

// SaveVIESValidation upserts a VIES validation result by country_code + vat_number.
func (m *MongoDB) SaveVIESValidation(v *entity.VIESValidation) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionVIESValidations)
	filter := bson.D{{"country_code", v.CountryCode}, {"vat_number", v.VATNumber}}
	update := bson.D{{"$set", v}}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	return err
}

// GetVIESValidation returns a cached VIES validation result by country_code + vat_number.
func (m *MongoDB) GetVIESValidation(countryCode, vatNumber string) (*entity.VIESValidation, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionVIESValidations)
	filter := bson.D{{"country_code", countryCode}, {"vat_number", vatNumber}}
	var v entity.VIESValidation
	err = collection.FindOne(ctx, filter).Decode(&v)
	if err != nil {
		return nil, m.findError(err)
	}
	return &v, nil
}

// SaveRetryJob upserts a retry job by _id (which equals EventId).
func (m *MongoDB) SaveRetryJob(job *entity.RetryJob) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionRetryJobs)
	filter := bson.D{{"_id", job.ID}}
	update := bson.D{{"$set", job}}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	return err
}

// GetPendingRetryJobs returns retry jobs that are pending and due for processing.
func (m *MongoDB) GetPendingRetryJobs() ([]*entity.RetryJob, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionRetryJobs)
	filter := bson.D{
		{"status", entity.RetryJobPending},
		{"next_retry_at", bson.D{{"$lte", time.Now()}}},
	}
	opts := options.Find().SetSort(bson.D{{"next_retry_at", 1}})
	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	var jobs []*entity.RetryJob
	err = cursor.All(ctx, &jobs)
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// GetAllPendingRetryJobs returns every retry job still in the pending state,
// regardless of whether it is due yet. Unlike GetPendingRetryJobs (which filters
// by next_retry_at for the processing loop), this is meant for operator inspection,
// so it returns the full pending backlog sorted by soonest next retry first.
func (m *MongoDB) GetAllPendingRetryJobs() ([]*entity.RetryJob, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionRetryJobs)
	filter := bson.D{{"status", entity.RetryJobPending}}
	opts := options.Find().SetSort(bson.D{{"next_retry_at", 1}})
	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	var jobs []*entity.RetryJob
	err = cursor.All(ctx, &jobs)
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// UpdateRetryJob replaces a retry job document by _id.
func (m *MongoDB) UpdateRetryJob(job *entity.RetryJob) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionRetryJobs)
	filter := bson.D{{"_id", job.ID}}
	_, err = collection.ReplaceOne(ctx, filter, job)
	return err
}

// GetRetryJobByEventId returns a retry job by its event_id.
func (m *MongoDB) GetRetryJobByEventId(eventId string) (*entity.RetryJob, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionRetryJobs)
	filter := bson.D{{"event_id", eventId}}
	var job entity.RetryJob
	err = collection.FindOne(ctx, filter).Decode(&job)
	if err != nil {
		return nil, m.findError(err)
	}
	return &job, nil
}

// SaveBankAccount upserts a wFirma company_account record by ID. Fields synced
// from wFirma overwrite existing values, but is_allowed is preserved on update
// (and defaults to false on first insert) so operator toggles survive re-sync.
func (m *MongoDB) SaveBankAccount(account *entity.BankAccount) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankAccounts)
	filter := bson.D{{"id", account.ID}}
	update := bson.D{
		{"$set", bson.D{
			{"id", account.ID},
			{"name", account.Name},
			{"bank_name", account.BankName},
			{"number", account.Number},
			{"swift", account.Swift},
			{"currency", account.Currency},
			{"status", account.Status},
			{"visibility", account.Visibility},
			{"synced_at", account.SyncedAt},
		}},
		{"$setOnInsert", bson.D{
			{"is_allowed", false},
		}},
	}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	return err
}

// GetAllowedBankAccount returns the single allowed bank account for the given
// currency, or nil (with no error) if none is marked allowed for that currency.
// If multiple are flagged for the same currency, the first match is returned —
// operators are expected to keep at most one allowed per currency.
func (m *MongoDB) GetAllowedBankAccount(currency string) (*entity.BankAccount, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankAccounts)
	filter := bson.D{{"currency", currency}, {"is_allowed", true}}
	var account entity.BankAccount
	err = collection.FindOne(ctx, filter).Decode(&account)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, fmt.Errorf("mongodb find: %w", err)
	}
	return &account, nil
}

// MigrateExistingTelegramUsers sets existing enabled users to RoleAdmin + TierRealtime (idempotent).
func (m *MongoDB) MigrateExistingTelegramUsers() error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{
		{"telegram_enabled", true},
		{"telegram_id", bson.D{{"$gt", 0}}},
		{"$or", bson.A{
			bson.D{{"telegram_role", bson.D{{"$exists", false}}}},
			bson.D{{"telegram_role", ""}},
		}},
	}
	update := bson.D{{"$set", bson.D{
		{"telegram_role", entity.RoleAdmin},
		{"subscription_tier", entity.TierRealtime},
	}}}
	_, err = collection.UpdateMany(ctx, filter, update)
	return err
}

// checkoutParamsDoc pairs a stored checkout params document with its Mongo _id so the
// dedupe migration can merge a group and delete the redundant rows by id.
type checkoutParamsDoc struct {
	ID                    primitive.ObjectID `bson:"_id"`
	entity.CheckoutParams `bson:",inline"`
}

// DedupeCheckoutParams collapses checkout_params documents that share an order_id into a
// single record, then enforces a partial unique index on order_id so duplicates cannot
// reappear. It is the one-off cleanup for records duplicated by the pre-fix write path
// (wFirma-only flows inserting a fresh document on every invoice/proforma call). It is
// idempotent: a second run finds no order_id with more than one document and only
// re-ensures the index. order_id is the canonical identity for the collection — it tracks
// one OpenCart order's lifecycle (see SaveCheckoutParams).
//
// Uses a dedicated, longer-lived context than opTimeout because a backlog of duplicate
// groups can take more than a single per-op deadline to clear; a partial run is safe since
// the merge is idempotent and the next startup continues.
func (m *MongoDB) DedupeCheckoutParams() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionCheckoutParams)

	// Find order_ids with more than one document. Empty/missing order_ids are excluded so
	// unrelated keyless records are never merged together.
	pipeline := mongo.Pipeline{
		{{"$match", bson.D{{"order_id", bson.D{{"$gt", ""}}}}}},
		{{"$group", bson.D{
			{"_id", "$order_id"},
			{"count", bson.D{{"$sum", 1}}},
		}}},
		{{"$match", bson.D{{"count", bson.D{{"$gt", 1}}}}}},
	}
	cursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		return fmt.Errorf("aggregate duplicate order ids: %w", err)
	}
	var groups []struct {
		OrderId string `bson:"_id"`
	}
	if err = cursor.All(ctx, &groups); err != nil {
		return fmt.Errorf("read duplicate order ids: %w", err)
	}

	for _, g := range groups {
		if err = m.mergeCheckoutParamsGroup(ctx, collection, g.OrderId); err != nil {
			return fmt.Errorf("merge order_id %s: %w", g.OrderId, err)
		}
	}

	return m.ensureCheckoutParamsIndex(ctx, collection)
}

// mergeCheckoutParamsGroup collapses all documents for one order_id into the most recently
// modified survivor, then deletes the rest. The survivor holds the freshest order data
// (client, line items, totals); resolution/linkage fields are backfilled from the rest of
// the group so a value recorded on an older row is never lost — session_id, payment_id,
// event_id, invoice_id/file, proforma_id/file, paid (logical OR), and the latest closed
// timestamp. created is pulled back to the earliest seen so the original order date stands.
func (m *MongoDB) mergeCheckoutParamsGroup(ctx context.Context, collection *mongo.Collection, orderId string) error {
	cursor, err := collection.Find(ctx, bson.D{{"order_id", orderId}}, options.Find().SetSort(bson.D{{"modified", -1}}))
	if err != nil {
		return err
	}
	var docs []checkoutParamsDoc
	if err = cursor.All(ctx, &docs); err != nil {
		return err
	}
	if len(docs) <= 1 {
		return nil
	}

	survivor := docs[0]
	merged := survivor.CheckoutParams
	for _, d := range docs[1:] {
		fillIfEmpty(&merged.SessionId, d.SessionId)
		fillIfEmpty(&merged.PaymentId, d.PaymentId)
		fillIfEmpty(&merged.EventId, d.EventId)
		fillIfEmpty(&merged.InvoiceId, d.InvoiceId)
		fillIfEmpty(&merged.InvoiceFile, d.InvoiceFile)
		fillIfEmpty(&merged.ProformaId, d.ProformaId)
		fillIfEmpty(&merged.ProformaFile, d.ProformaFile)
		if d.Paid {
			merged.Paid = true
		}
		if d.Closed.After(merged.Closed) {
			merged.Closed = d.Closed
		}
		if !d.Created.IsZero() && (merged.Created.IsZero() || d.Created.Before(merged.Created)) {
			merged.Created = d.Created
		}
	}
	merged.Modified = time.Now()

	if _, err = collection.ReplaceOne(ctx, bson.D{{"_id", survivor.ID}}, merged); err != nil {
		return err
	}

	redundant := make([]primitive.ObjectID, 0, len(docs)-1)
	for _, d := range docs[1:] {
		redundant = append(redundant, d.ID)
	}
	_, err = collection.DeleteMany(ctx, bson.D{{"_id", bson.D{{"$in", redundant}}}})
	return err
}

// ensureCheckoutParamsIndex creates a partial unique index on order_id so a second document
// for the same order can never be inserted. The partial filter (order_id is a non-empty
// string) excludes the rare keyless record, which would otherwise collide on a null key.
// Idempotent — re-creating an identical index is a no-op.
func (m *MongoDB) ensureCheckoutParamsIndex(ctx context.Context, collection *mongo.Collection) error {
	model := mongo.IndexModel{
		Keys: bson.D{{"order_id", 1}},
		Options: options.Index().
			SetName("uniq_order_id").
			SetUnique(true).
			SetPartialFilterExpression(bson.D{{"order_id", bson.D{{"$gt", ""}}}}),
	}
	_, err := collection.Indexes().CreateOne(ctx, model)
	return err
}

// fillIfEmpty copies src into dst only when dst is empty and src is not, used to backfill
// linkage fields during the checkout params dedupe without overwriting an existing value.
func fillIfEmpty(dst *string, src string) {
	if *dst == "" && src != "" {
		*dst = src
	}
}
