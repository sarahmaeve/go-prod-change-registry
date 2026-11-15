package postgres_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/postgres"
)

// An RDS DSN without a password is an IAM login; both connection paths must
// refuse one that would send its token without verifying the server. The
// refusal comes before any network I/O.
const unverifiedIAMDSN = "postgres://pcr_app@pcr.abcdefghijkl.eu-west-1.rds.amazonaws.com:5432/pcr?sslmode=require"

func TestOpenRefusesUnverifiedIAMLogin(t *testing.T) {
	t.Parallel()
	pool, err := postgres.Open(t.Context(), unverifiedIAMDSN, postgres.PoolOptions{
		MaxConnections: 1,
		ConnectTimeout: time.Second,
	})
	if pool != nil {
		pool.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Errorf("Open() err = %v, want a refusal naming verify-full", err)
	}
}

func TestMigrateRefusesUnverifiedIAMLogin(t *testing.T) {
	t.Parallel()
	err := postgres.Migrate(unverifiedIAMDSN, time.Second)
	if err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Errorf("Migrate() err = %v, want a refusal naming verify-full", err)
	}
}
