package database

import (
	"context"
	"errors"
	"fmt"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	collectionUsers          = "users"
	collectionCheckoutParams = "checkout_params"
	collectionInvoice        = "wfirma_invoice"
	collectionProducts       = "products"
)

type MongoDB struct {
	ctx           context.Context
	clientOptions *options.ClientOptions
	database      string
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
		ctx:           context.Background(),
		clientOptions: clientOptions,
		database:      conf.Mongo.Database,
	}
	return client
}

func (m *MongoDB) connect() (*mongo.Client, error) {
	connection, err := mongo.Connect(m.ctx, m.clientOptions)
	if err != nil {
		return nil, fmt.Errorf("mongodb connect: %w", err)
	}
	return connection, nil
}

func (m *MongoDB) disconnect(connection *mongo.Client) {
	_ = connection.Disconnect(m.ctx)
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
	connection, err := m.connect()
	if err != nil {
		return err
	}
	defer m.disconnect(connection)

	collection := connection.Database(m.database).Collection(key)
	_, err = collection.InsertOne(m.ctx, value)
	return err
}

func (m *MongoDB) GetUser(token string) (*entity.User, error) {
	connection, err := m.connect()
	if err != nil {
		return nil, err
	}
	defer m.disconnect(connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"token", token}}
	var user entity.User
	err = collection.FindOne(m.ctx, filter).Decode(&user)
	return &user, err
}

func (m *MongoDB) GetTelegramUsers() ([]*entity.User, error) {
	connection, err := m.connect()
	if err != nil {
		return nil, err
	}
	defer m.disconnect(connection)

	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_id", bson.D{{"$gt", 0}}}, {"telegram_enabled", true}}
	cursor, err := collection.Find(m.ctx, filter)
	if err != nil {
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, m.ctx)

	var users []*entity.User
	err = cursor.All(m.ctx, &users)
	if err != nil {
		return nil, err
	}
	return users, nil
}

func (m *MongoDB) SetTelegramEnabled(id int64, isActive bool, logLevel int) error {
	connection, err := m.connect()
	if err != nil {
		return err
	}
	defer m.disconnect(connection)
	collection := connection.Database(m.database).Collection(collectionUsers)
	filter := bson.D{{"telegram_id", id}}
	update := bson.D{{"$set", bson.D{
		{"telegram_enabled", isActive},
		{"log_level", logLevel},
	}}}
	_, err = collection.UpdateOne(m.ctx, filter, update)
	return err
}

func (m *MongoDB) SaveCheckoutParams(params *entity.CheckoutParams) error {
	connection, err := m.connect()
	if err != nil {
		return err
	}
	defer m.disconnect(connection)

	if params.Created.IsZero() {
		params.Created = time.Now()
	}
	params.Modified = time.Now()

	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	_, err = collection.InsertOne(m.ctx, params)
	return err
}

func (m *MongoDB) UpdateCheckoutParams(params *entity.CheckoutParams) error {
	connection, err := m.connect()
	if err != nil {
		return err
	}
	defer m.disconnect(connection)

	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	filter := bson.D{{"order_id", params.OrderId}}
	update := bson.D{{"$set", bson.D{
		{"invoice_id", params.InvoiceId},
		{"proforma_id", params.ProformaId},
		{"closed", time.Now()},
	}}}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(m.ctx, filter, update, opts)
	return err
}

func (m *MongoDB) GetCheckoutParamsForEvent(eventId string) (*entity.CheckoutParams, error) {
	connection, err := m.connect()
	if err != nil {
		return nil, err
	}
	defer m.disconnect(connection)
	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	filter := bson.D{{"event_id", eventId}}
	var params entity.CheckoutParams
	err = collection.FindOne(m.ctx, filter).Decode(&params)
	if err != nil {
		return nil, m.findError(err)
	}
	return &params, nil
}

func (m *MongoDB) GetCheckoutParamsSession(sessionId string) (*entity.CheckoutParams, error) {
	connection, err := m.connect()
	if err != nil {
		return nil, err
	}
	defer m.disconnect(connection)
	collection := connection.Database(m.database).Collection(collectionCheckoutParams)
	filter := bson.D{{"session_id", sessionId}}
	var params entity.CheckoutParams
	err = collection.FindOne(m.ctx, filter).Decode(&params)
	if err != nil {
		return nil, m.findError(err)
	}
	return &params, nil
}

func (m *MongoDB) GetProductBySku(sku string) (*entity.Product, error) {
	connection, err := m.connect()
	if err != nil {
		return nil, err
	}
	defer m.disconnect(connection)

	collection := connection.Database(m.database).Collection(collectionProducts)
	filter := bson.D{{"sku", sku}}
	var product entity.Product
	err = collection.FindOne(m.ctx, filter).Decode(&product)
	if err != nil {
		return nil, m.findError(err)
	}
	return &product, nil
}

func (m *MongoDB) SaveProduct(product *entity.Product) error {
	connection, err := m.connect()
	if err != nil {
		return err
	}
	defer m.disconnect(connection)

	collection := connection.Database(m.database).Collection(collectionProducts)
	filter := bson.D{{"sku", product.Sku}}
	update := bson.D{{"$set", product}}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(m.ctx, filter, update, opts)
	return err
}

func (m *MongoDB) SaveInvoice(id string, invoice interface{}) error {
	connection, err := m.connect()
	if err != nil {
		return err
	}
	defer m.disconnect(connection)

	collection := connection.Database(m.database).Collection(collectionInvoice)
	filter := bson.D{{"id", id}}
	update := bson.D{{"$set", invoice}}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(m.ctx, filter, update, opts)
	return err
}
