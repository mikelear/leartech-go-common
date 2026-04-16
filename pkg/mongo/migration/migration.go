// Package migration provides a simple MongoDB migration framework.
// Replaces spring-financial-group/mqa/pkg/mongo/migration.
package migration

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog/log"

	learmongo "github.com/mikelear/leartech-go-common/pkg/mongo"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// BaseModel provides common audit fields for MongoDB documents.
// Embed this in your document structs.
type BaseModel struct {
	ID        string    `bson:"_id,omitempty" json:"id"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	CreatedBy string    `bson:"createdBy,omitempty" json:"createdBy,omitempty"`
	UpdatedBy string    `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
	Deleted   bool      `bson:"deleted" json:"deleted"`
}

// SetTimestamps sets both created and updated timestamps to now.
func (b *BaseModel) SetTimestamps() {
	now := time.Now().UTC()
	b.CreatedAt = now
	b.UpdatedAt = now
}

// UpdateTimestamps sets the updated timestamp to now.
func (b *BaseModel) UpdateTimestamps() {
	b.UpdatedAt = time.Now().UTC()
}

// Migration defines a single migration step.
type Migration struct {
	Version     string
	Description string
	Up          func(ctx context.Context, db learmongo.MongoDatabase) error
}

// MigrationManager runs migrations in order, tracking applied versions.
type MigrationManager struct {
	client     learmongo.MongoClient
	dbName     string
	migrations []Migration
}

// NewMigrationManager creates a new migration manager.
func NewMigrationManager(client learmongo.MongoClient, dbName string) *MigrationManager {
	return &MigrationManager{
		client: client,
		dbName: dbName,
	}
}

// Register adds a migration to the manager.
func (m *MigrationManager) Register(migration Migration) {
	m.migrations = append(m.migrations, migration)
}

type migrationRecord struct {
	Version   string    `bson:"_id"`
	AppliedAt time.Time `bson:"appliedAt"`
}

// RunMigrations applies all unapplied migrations in version order.
func (m *MigrationManager) RunMigrations(ctx context.Context, limit int) error {
	db := m.client.Database(m.dbName)
	migrationsColl := db.Collection("_migrations")

	// Get applied versions
	applied := make(map[string]bool)
	cursor, err := migrationsColl.Find(ctx, bson.M{})
	if err == nil {
		defer cursor.Close(ctx)
		for cursor.Next(ctx) {
			var record migrationRecord
			if err := cursor.Decode(&record); err == nil {
				applied[record.Version] = true
			}
		}
	}

	// Sort migrations by version
	sort.Slice(m.migrations, func(i, j int) bool {
		return m.migrations[i].Version < m.migrations[j].Version
	})

	ran := 0
	for _, mig := range m.migrations {
		if ran >= limit {
			break
		}
		if applied[mig.Version] {
			continue
		}

		log.Info().Str("version", mig.Version).Str("description", mig.Description).Msg("running migration")

		if err := mig.Up(ctx, db); err != nil {
			return fmt.Errorf("migration %s failed: %w", mig.Version, err)
		}

		record := migrationRecord{
			Version:   mig.Version,
			AppliedAt: time.Now().UTC(),
		}
		if _, err := migrationsColl.InsertOne(ctx, record); err != nil {
			return fmt.Errorf("recording migration %s: %w", mig.Version, err)
		}

		ran++
		log.Info().Str("version", mig.Version).Msg("migration applied")
	}

	return nil
}
