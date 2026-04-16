// Package mongo provides MongoDB client abstractions and connection helpers.
// Replaces spring-financial-group/mqa/pkg/domain and spring-financial-group/mqa/pkg/mongo.
package mongo

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoClient wraps the MongoDB client.
type MongoClient interface {
	Database(name string) MongoDatabase
	Disconnect(ctx context.Context) error
}

// MongoDatabase wraps a MongoDB database.
type MongoDatabase interface {
	Collection(name string) MongoCollection
}

// MongoCollection wraps a MongoDB collection with typed operations.
type MongoCollection interface {
	FindOne(ctx context.Context, filter interface{}) *mongo.SingleResult
	Find(ctx context.Context, filter interface{}, opts ...options.Lister[options.FindOptions]) (*mongo.Cursor, error)
	InsertOne(ctx context.Context, document interface{}) (*mongo.InsertOneResult, error)
	InsertMany(ctx context.Context, documents []interface{}) (*mongo.InsertManyResult, error)
	ReplaceOne(ctx context.Context, filter interface{}, replacement interface{}) (*mongo.UpdateResult, error)
	UpdateOne(ctx context.Context, filter interface{}, update interface{}) (*mongo.UpdateResult, error)
	DeleteOne(ctx context.Context, filter interface{}) (*mongo.DeleteResult, error)
	DeleteMany(ctx context.Context, filter interface{}) (*mongo.DeleteResult, error)
	Aggregate(ctx context.Context, pipeline interface{}) (*mongo.Cursor, error)
	CountDocuments(ctx context.Context, filter interface{}) (int64, error)
	CreateIndexes(ctx context.Context, indexes []mongo.IndexModel) ([]string, error)
}

// MongoIndex defines an index to create.
type MongoIndex struct {
	Keys   bson.D
	Unique bool
}
