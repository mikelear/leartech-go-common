package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type clientWrapper struct {
	client *mongo.Client
}

type databaseWrapper struct {
	db *mongo.Database
}

type collectionWrapper struct {
	coll *mongo.Collection
}

// NewClient creates a connected MongoDB client.
func NewClient(connectionString string) (MongoClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(connectionString))
	if err != nil {
		return nil, fmt.Errorf("connecting to MongoDB: %w", err)
	}

	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("pinging MongoDB: %w", err)
	}

	return &clientWrapper{client: client}, nil
}

func (c *clientWrapper) Database(name string) MongoDatabase {
	return &databaseWrapper{db: c.client.Database(name)}
}

func (c *clientWrapper) Disconnect(ctx context.Context) error {
	return c.client.Disconnect(ctx)
}

func (d *databaseWrapper) Collection(name string) MongoCollection {
	return &collectionWrapper{coll: d.db.Collection(name)}
}

// Collection method implementations — delegate to the underlying mongo.Collection.

func (c *collectionWrapper) FindOne(ctx context.Context, filter interface{}) *mongo.SingleResult {
	return c.coll.FindOne(ctx, filter)
}

func (c *collectionWrapper) Find(ctx context.Context, filter interface{}, opts ...options.Lister[options.FindOptions]) (*mongo.Cursor, error) {
	return c.coll.Find(ctx, filter, opts...)
}

func (c *collectionWrapper) InsertOne(ctx context.Context, document interface{}) (*mongo.InsertOneResult, error) {
	return c.coll.InsertOne(ctx, document)
}

func (c *collectionWrapper) InsertMany(ctx context.Context, documents []interface{}) (*mongo.InsertManyResult, error) {
	return c.coll.InsertMany(ctx, documents)
}

func (c *collectionWrapper) ReplaceOne(ctx context.Context, filter interface{}, replacement interface{}) (*mongo.UpdateResult, error) {
	return c.coll.ReplaceOne(ctx, filter, replacement)
}

func (c *collectionWrapper) UpdateOne(ctx context.Context, filter interface{}, update interface{}) (*mongo.UpdateResult, error) {
	return c.coll.UpdateOne(ctx, filter, update)
}

func (c *collectionWrapper) DeleteOne(ctx context.Context, filter interface{}) (*mongo.DeleteResult, error) {
	return c.coll.DeleteOne(ctx, filter)
}

func (c *collectionWrapper) DeleteMany(ctx context.Context, filter interface{}) (*mongo.DeleteResult, error) {
	return c.coll.DeleteMany(ctx, filter)
}

func (c *collectionWrapper) Aggregate(ctx context.Context, pipeline interface{}) (*mongo.Cursor, error) {
	return c.coll.Aggregate(ctx, pipeline)
}

func (c *collectionWrapper) CountDocuments(ctx context.Context, filter interface{}) (int64, error) {
	return c.coll.CountDocuments(ctx, filter)
}

func (c *collectionWrapper) CreateIndexes(ctx context.Context, indexes []mongo.IndexModel) ([]string, error) {
	return c.coll.Indexes().CreateMany(ctx, indexes)
}
