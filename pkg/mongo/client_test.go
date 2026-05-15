package mongo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient_RejectsInvalidURI(t *testing.T) {
	c, err := NewClient("not-a-mongo-uri")
	require.Error(t, err)
	assert.Nil(t, c)
}

func TestNewClient_FailsToConnect(t *testing.T) {
	// Well-formed URI but nothing listens on this port — exercises the Ping branch.
	c, err := NewClient("mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=200")
	require.Error(t, err)
	assert.Nil(t, c)
	assert.Contains(t, err.Error(), "pinging MongoDB")
}
