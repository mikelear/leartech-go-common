package auth

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
)

// validateAudience covers both happy and unhappy paths for RFC 8707
// audience binding. Mirrors rust + dotnet templates' AuthLayer
// behaviour, fixes go-common's previous lenient acceptance.
func TestValidateAudience_AcceptsArrayWithMatch(t *testing.T) {
	mc := jwt.MapClaims{
		"aud": []interface{}{
			"leartech-rust-service-template",
			"leartech-go-service-template",
			"leartech-dotnet-service-template",
		},
	}
	assert.NoError(t, validateAudience(mc, "leartech-go-service-template"))
}

func TestValidateAudience_AcceptsStringMatch(t *testing.T) {
	mc := jwt.MapClaims{"aud": "leartech-go-service-template"}
	assert.NoError(t, validateAudience(mc, "leartech-go-service-template"))
}

func TestValidateAudience_RejectsMissingClaim(t *testing.T) {
	mc := jwt.MapClaims{"sub": "user-123"} // no aud at all
	err := validateAudience(mc, "leartech-go-service-template")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing 'aud'")
}

func TestValidateAudience_RejectsEmptyArray(t *testing.T) {
	// This is the exact case that masked the audience-on-refresh bug —
	// Hydra dropped the audience binding on refresh, issuing a token
	// with aud=[]. go-common's lenient validation accepted it; rust +
	// dotnet correctly rejected. Strict validation now rejects too.
	mc := jwt.MapClaims{"aud": []interface{}{}}
	err := validateAudience(mc, "leartech-go-service-template")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not contain")
}

func TestValidateAudience_RejectsArrayWithoutMatch(t *testing.T) {
	mc := jwt.MapClaims{
		"aud": []interface{}{"leartech-rust-service-template", "leartech-dotnet-service-template"},
	}
	err := validateAudience(mc, "leartech-go-service-template")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not contain")
}

func TestValidateAudience_RejectsStringMismatch(t *testing.T) {
	mc := jwt.MapClaims{"aud": "leartech-rust-service-template"}
	err := validateAudience(mc, "leartech-go-service-template")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

func TestValidateAudience_RejectsUnexpectedType(t *testing.T) {
	mc := jwt.MapClaims{"aud": 42} // bizarre but possible if attacker-crafted
	err := validateAudience(mc, "leartech-go-service-template")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected type")
}
