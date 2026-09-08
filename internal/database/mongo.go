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
	collectionBankSessions    = "bank_sessions"
	collectionBankTx          = "bank_transactions"
	collectionPaymentFacts    = "payment_facts"
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

// GetCheckoutParamsByDateRange returns every checkout params document created within
// the given day range (inclusive, YYYY-MM-DD). This is the only store that knows about
// orders from all sources — OpenCart, B2B portal and direct API callers alike — so the
// invoice list uses it to report orders that exist nowhere else, including those whose
// invoice failed and is still sitting in the retry queue.
func (m *MongoDB) GetCheckoutParamsByDateRange(from, to string) ([]*entity.CheckoutParams, error) {
	start, err := time.Parse(entity.DateLayout, from)
	if err != nil {
		return nil, fmt.Errorf("parse from date: %w", err)
	}
	end, err := time.Parse(entity.DateLayout, to)
	if err != nil {
		return nil, fmt.Errorf("parse to date: %w", err)
	}
	// `to` is an inclusive day, so the upper bound is the start of the following day.
	end = end.AddDate(0, 0, 1)

	filter := bson.D{
		{"created", bson.D{{"$gte", start}, {"$lt", end}}},
	}
	return m.findCheckoutParams(filter)
}

// GetCheckoutParamsByRefs returns checkout params whose order_id or external_id is in
// refs. Both keys are matched because an order's external reference (the value stamped
// into the wFirma id_external) differs from its order id for systems with their own id
// space — the B2B portal keys on the order UID. Used to resolve orders whose invoice
// falls in the requested range but which were created before it.
func (m *MongoDB) GetCheckoutParamsByRefs(refs []string) ([]*entity.CheckoutParams, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	filter := bson.D{
		{"$or", bson.A{
			bson.D{{"order_id", bson.D{{"$in", refs}}}},
			bson.D{{"external_id", bson.D{{"$in", refs}}}},
		}},
	}
	return m.findCheckoutParams(filter)
}

// findCheckoutParams runs a filter against the checkout_params collection and decodes
// the full documents.
func (m *MongoDB) findCheckoutParams(filter bson.D) ([]*entity.CheckoutParams, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	cursor, err := collection.Find(ctx, filter, options.Find().SetSort(bson.D{{"created", 1}}))
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	var result []*entity.CheckoutParams
	if err = cursor.All(ctx, &result); err != nil {
		return nil, err
	}
	return result, nil
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

// GetRetryJobsByOrderId returns every retry job recorded for an order, newest first,
// regardless of status. Status is deliberately not filtered: an operator triggering a
// manual retry is usually looking at a job that already exhausted its attempts and is
// therefore no longer pending.
func (m *MongoDB) GetRetryJobsByOrderId(orderId string) ([]*entity.RetryJob, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionRetryJobs)
	filter := bson.D{{"order_id", orderId}}
	opts := options.Find().SetSort(bson.D{{"created_at", -1}})
	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	var jobs []*entity.RetryJob
	if err = cursor.All(ctx, &jobs); err != nil {
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

// SaveBankSession upserts a bank authorization session, keyed by its state value.
func (m *MongoDB) SaveBankSession(session *entity.BankSession) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankSessions)
	filter := bson.D{{Key: "state", Value: session.State}}
	update := bson.D{{Key: "$set", Value: session}}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	return err
}

// GetBankSessionByState loads the session started under the given state value.
// It returns nil without error when no such authorization was started, so a stray
// callback is rejected rather than treated as a failure.
func (m *MongoDB) GetBankSessionByState(state string) (*entity.BankSession, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankSessions)
	filter := bson.D{{Key: "state", Value: state}}
	var session entity.BankSession
	if err := collection.FindOne(ctx, filter).Decode(&session); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

// GetActiveBankSession returns the authorized session with the latest expiry, which is
// the one polling should use. It returns nil without error when no account has been
// authorized yet.
func (m *MongoDB) GetActiveBankSession() (*entity.BankSession, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankSessions)
	filter := bson.D{{Key: "status", Value: entity.BankSessionActive}}
	opts := options.FindOne().SetSort(bson.D{{Key: "valid_until", Value: -1}})
	var session entity.BankSession
	if err := collection.FindOne(ctx, filter, opts).Decode(&session); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

// SupersedeBankSessions marks every active session other than keepState as revoked.
// Called after a fresh authorization so that polling never has two live consents to
// choose between.
func (m *MongoDB) SupersedeBankSessions(keepState string) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankSessions)
	filter := bson.D{
		{Key: "status", Value: entity.BankSessionActive},
		{Key: "state", Value: bson.D{{Key: "$ne", Value: keepState}}},
	}
	update := bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: entity.BankSessionRevoked}}}}
	_, err = collection.UpdateMany(ctx, filter, update)
	return err
}

// SaveBankTransactions stores a fetched window of account movements, returning how many
// were new.
//
// Writes are upserts keyed by BankTransaction.Key: every poll re-reads a sliding window
// and therefore re-delivers entries already stored, because banks can book entries with
// a back-date and polling "since the last one seen" would silently miss them. The unique
// index makes that safe rather than merely tidy — two pollers, or a retried poll, cannot
// double-insert.
func (m *MongoDB) SaveBankTransactions(txs []*entity.BankTransaction) (int, error) {
	if len(txs) == 0 {
		return 0, nil
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return 0, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankTx)
	if err := m.ensureBankTxIndex(ctx, collection); err != nil {
		return 0, fmt.Errorf("ensure bank tx index: %w", err)
	}

	models := make([]mongo.WriteModel, 0, len(txs))
	for _, t := range txs {
		if t == nil || t.Key == "" {
			continue
		}
		// $setOnInsert, not $set: a booked entry never changes, but the matcher writes
		// match_status onto the same row — and the sliding window re-delivers that row
		// on every poll, so an unconditional write would erase the match every cycle.
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.D{{Key: "key", Value: t.Key}}).
			SetUpdate(bson.D{{Key: "$setOnInsert", Value: t}}).
			SetUpsert(true))
	}
	if len(models) == 0 {
		return 0, nil
	}

	// Unordered so one rejected entry does not abandon the rest of the window.
	res, err := collection.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	if err != nil {
		return 0, err
	}
	return int(res.UpsertedCount), nil
}

// ensureBankTxIndex creates the unique index on the idempotency key. Idempotent.
func (m *MongoDB) ensureBankTxIndex(ctx context.Context, collection *mongo.Collection) error {
	_, err := collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "key", Value: 1}},
		Options: options.Index().
			SetName("uniq_key").
			SetUnique(true),
	})
	return err
}

// GetBankTransactionsByDateRange returns stored movements booked within [from, to],
// oldest first. Dates are "YYYY-MM-DD", which sorts correctly as a string.
func (m *MongoDB) GetBankTransactionsByDateRange(from, to string) ([]*entity.BankTransaction, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankTx)
	filter := bson.D{
		{Key: "booking_date", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}},
	}
	opts := options.Find().SetSort(bson.D{{Key: "booking_date", Value: 1}})
	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var txs []*entity.BankTransaction
	if err := cursor.All(ctx, &txs); err != nil {
		return nil, err
	}
	return txs, nil
}

// MarkBankSessionPolled records a successful poll, so the session view shows whether
// statements are actually flowing rather than only that a consent exists.
func (m *MongoDB) MarkBankSessionPolled(state string, at time.Time) error {
	return m.setBankSessionFields(state, bson.D{{Key: "last_polled_at", Value: at}})
}

// MarkBankSessionExpired flags a session whose consent the bank has stopped honouring.
// Polling stops until the account holder authorizes again — there is no refresh.
func (m *MongoDB) MarkBankSessionExpired(state, reason string) error {
	return m.setBankSessionFields(state, bson.D{
		{Key: "status", Value: entity.BankSessionExpired},
		{Key: "failure_reason", Value: reason},
	})
}

// MarkBankSessionWarned records when an expiry warning was last sent, so the reminder
// repeats daily instead of on every poll.
func (m *MongoDB) MarkBankSessionWarned(state string, at time.Time) error {
	return m.setBankSessionFields(state, bson.D{{Key: "last_warned_at", Value: at}})
}

func (m *MongoDB) setBankSessionFields(state string, fields bson.D) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankSessions)
	_, err = collection.UpdateOne(ctx,
		bson.D{{Key: "state", Value: state}},
		bson.D{{Key: "$set", Value: fields}})
	return err
}

// GetInvoicesByNumbers loads documents by their wFirma number, with no date bound.
//
// Deliberately unbounded in time: customers pay invoices months after they were issued —
// the August statement settled documents numbered from earlier in the year — so a date
// window here would drop correct matches. The number identifies one document on its own.
func (m *MongoDB) GetInvoicesByNumbers(numbers []string) ([]*entity.LocalInvoice, error) {
	if len(numbers) == 0 {
		return nil, nil
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionInvoice)
	filter := bson.D{{Key: "number", Value: bson.D{{Key: "$in", Value: numbers}}}}
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var invoices []*entity.LocalInvoice
	if err := cursor.All(ctx, &invoices); err != nil {
		return nil, err
	}
	return invoices, nil
}

// GetInvoicesByExternalRefs loads documents by the order reference wFirma stores in
// id_external. Used when a payment names an order number rather than a document number.
func (m *MongoDB) GetInvoicesByExternalRefs(refs []string) ([]*entity.LocalInvoice, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionInvoice)
	filter := bson.D{{Key: "id_external", Value: bson.D{{Key: "$in", Value: refs}}}}
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var invoices []*entity.LocalInvoice
	if err := cursor.All(ctx, &invoices); err != nil {
		return nil, err
	}
	return invoices, nil
}

// GetUnmatchedBankTransactions returns incoming payments the matcher has not resolved,
// oldest first. Only credits are considered: money leaving the account settles nothing.
func (m *MongoDB) GetUnmatchedBankTransactions(limit int) ([]*entity.BankTransaction, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankTx)
	filter := bson.D{
		{Key: "direction", Value: entity.DirectionCredit},
		{Key: "$or", Value: []bson.D{
			{{Key: "match_status", Value: entity.BankMatchUnmatched}},
			{{Key: "match_status", Value: bson.D{{Key: "$exists", Value: false}}}},
		}},
	}
	opts := options.Find().SetSort(bson.D{{Key: "booking_date", Value: 1}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var txs []*entity.BankTransaction
	if err := cursor.All(ctx, &txs); err != nil {
		return nil, err
	}
	return txs, nil
}

// GetBankTransactionByKey loads one statement entry.
func (m *MongoDB) GetBankTransactionByKey(key string) (*entity.BankTransaction, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankTx)
	var tx entity.BankTransaction
	if err := collection.FindOne(ctx, bson.D{{Key: "key", Value: key}}).Decode(&tx); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &tx, nil
}

// SetBankTransactionMatch records the outcome of matching one entry. It touches only the
// match fields, leaving the statement record as the bank reported it.
func (m *MongoDB) SetBankTransactionMatch(key, status, ref string) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionBankTx)
	_, err = collection.UpdateOne(ctx,
		bson.D{{Key: "key", Value: key}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "match_status", Value: status},
			{Key: "matched_ref", Value: ref},
		}}})
	return err
}

// SavePaymentFact upserts the settlement record for one order, keyed by its external ref.
func (m *MongoDB) SavePaymentFact(fact *entity.PaymentFact) error {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionPaymentFacts)
	if err := m.ensurePaymentFactIndex(ctx, collection); err != nil {
		return fmt.Errorf("ensure payment fact index: %w", err)
	}
	_, err = collection.UpdateOne(ctx,
		bson.D{{Key: "external_ref", Value: fact.ExternalRef}},
		bson.D{{Key: "$set", Value: fact}},
		options.Update().SetUpsert(true))
	return err
}

// ensurePaymentFactIndex enforces one settlement record per order. Idempotent.
func (m *MongoDB) ensurePaymentFactIndex(ctx context.Context, collection *mongo.Collection) error {
	_, err := collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "external_ref", Value: 1}},
		Options: options.Index().SetName("uniq_external_ref").SetUnique(true),
	})
	return err
}

// GetPaymentFactsByRefs returns settlement records for the given orders. This is what the
// B2B portal reads: it asks about the orders it knows, in one request rather than one per
// order.
func (m *MongoDB) GetPaymentFactsByRefs(refs []string) ([]*entity.PaymentFact, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionPaymentFacts)
	filter := bson.D{{Key: "external_ref", Value: bson.D{{Key: "$in", Value: refs}}}}
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var facts []*entity.PaymentFact
	if err := cursor.All(ctx, &facts); err != nil {
		return nil, err
	}
	return facts, nil
}

// GetPaymentFactsByDateRange returns settlement records whose payment landed within
// [from, to]. Used by the ERP feed.
func (m *MongoDB) GetPaymentFactsByDateRange(from, to time.Time) ([]*entity.PaymentFact, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	connection, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer m.disconnect(ctx, connection)

	collection := connection.Database(m.database).Collection(collectionPaymentFacts)
	filter := bson.D{{Key: "paid_at", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}}}
	opts := options.Find().SetSort(bson.D{{Key: "paid_at", Value: 1}})
	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var facts []*entity.PaymentFact
	if err := cursor.All(ctx, &facts); err != nil {
		return nil, err
	}
	return facts, nil
}
