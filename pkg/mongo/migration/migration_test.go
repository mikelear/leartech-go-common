package migration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBaseModel_SetTimestamps(t *testing.T) {
	var b BaseModel
	before := time.Now().UTC()
	b.SetTimestamps()
	after := time.Now().UTC()

	assert.False(t, b.CreatedAt.Before(before))
	assert.False(t, b.CreatedAt.After(after))
	assert.Equal(t, b.CreatedAt, b.UpdatedAt, "SetTimestamps sets both equal")
}

func TestBaseModel_UpdateTimestamps_LeavesCreatedAlone(t *testing.T) {
	created := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	b := BaseModel{CreatedAt: created}

	b.UpdateTimestamps()

	assert.Equal(t, created, b.CreatedAt, "CreatedAt must not move")
	assert.True(t, b.UpdatedAt.After(created), "UpdatedAt should advance to now")
}

func TestManager_RegisterAndOrder(t *testing.T) {
	// Manager is exposed via NewManager(client, dbName), but Register doesn't
	// touch the client — exercising it with a nil client is safe for this case.
	m := &Manager{}
	m.Register(Migration{Version: "0002"})
	m.Register(Migration{Version: "0001"})
	m.Register(Migration{Version: "0003"})

	assert.Len(t, m.migrations, 3)
	// Register doesn't sort — RunMigrations does. Confirm raw order is preserved
	// so we know future refactors don't move the sort.
	assert.Equal(t, "0002", m.migrations[0].Version)
	assert.Equal(t, "0001", m.migrations[1].Version)
	assert.Equal(t, "0003", m.migrations[2].Version)
}

func TestNewManager_StoresFields(t *testing.T) {
	m := NewManager(nil, "mydb")
	assert.Equal(t, "mydb", m.dbName)
	assert.Nil(t, m.client)
	assert.Empty(t, m.migrations)
}
