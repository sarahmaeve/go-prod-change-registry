package rdsiam //nolint:testpackage // white-box tests read the embedded CA bundle and the unexported region parser

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/jackc/pgx/v5"
)

const rdsHost = "pcr.abcdefghijkl.eu-west-1.rds.amazonaws.com"

var testAWS = &aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "")}

func parse(t *testing.T, dsn string) *pgx.ConnConfig {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPasswordAndLocalDSNsAreLeftAlone(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{
		"postgres://pcr@127.0.0.1/pcr?sslmode=disable",
		"postgres://pcr:password@postgres.example.internal:5432/pcr?sslmode=require",
		"postgres://pcr:secret@" + rdsHost + ":5432/pcr?sslmode=require",
		"postgres://pcr:secret@" + rdsHost + ":5432/pcr?sslmode=verify-full",
	} {
		cfg := parse(t, dsn)
		var roots *x509.CertPool
		if cfg.TLSConfig != nil {
			roots = cfg.TLSConfig.RootCAs
		}
		hook, err := Configure(t.Context(), cfg, testAWS)
		if err != nil || hook != nil {
			t.Errorf("%s: hook=%v err=%v, want a plain login", dsn, hook != nil, err)
		}
		if cfg.TLSConfig != nil && cfg.TLSConfig.RootCAs != roots {
			t.Errorf("%s: Configure changed the TLS roots of a plain login", dsn)
		}
	}
}

// TestIAMRefusesUnverifiedTLS: verify-ca checks the chain but not the host name (pgx
// skips Go's verification and runs its own), so it's refused like the rest. So is
// require with an sslrootcert, which libpq and pgx treat as verify-ca, and a
// multi-host DSN, whose fallbacks pgx tries on its own.
func TestIAMRefusesUnverifiedTLS(t *testing.T) {
	t.Parallel()
	rootCert := writeBundle(t)
	base := "postgres://pcr_app@" + rdsHost + ":5432/pcr?sslmode="
	for _, dsn := range []string{
		base + "require",
		base + "prefer",
		base + "allow",
		base + "verify-ca",
		base + "disable",
		base + "require&sslrootcert=" + rootCert,
		"postgres://pcr_app@" + rdsHost + ":5432," + strings.Replace(rdsHost, "pcr.", "pcr-2.", 1) + ":5432/pcr?sslmode=verify-full",
	} {
		_, err := Configure(t.Context(), parse(t, dsn), testAWS)
		if err == nil || !strings.Contains(err.Error(), "verify-full") {
			t.Errorf("%s: err=%v, want a refusal naming verify-full", dsn, err)
		}
	}
}

func TestIAMSetsAFreshTokenOnEveryConnection(t *testing.T) {
	t.Parallel()
	cfg := parse(t, "postgres://pcr_app@"+rdsHost+":5432/pcr?sslmode=verify-full")
	hook, err := Configure(t.Context(), cfg, testAWS)
	if err != nil || hook == nil {
		t.Fatalf("hook=%v err=%v, want an IAM login", hook != nil, err)
	}
	if cfg.TLSConfig.RootCAs == nil || cfg.TLSConfig.ServerName != rdsHost {
		t.Fatalf("TLS not verifying %s against the RDS roots", rdsHost)
	}
	// pgxpool and stdlib both hand the hook a copy of the config per new connection.
	for range 2 {
		cc := cfg.Copy()
		if err := hook(t.Context(), cc); err != nil {
			t.Fatal(err)
		}
		// The token is a presigned URL for this endpoint and user, signed for the
		// endpoint's region (eu-west-1), not the AWS config's.
		for _, want := range []string{rdsHost + ":5432?", "Action=connect", "DBUser=pcr_app", "eu-west-1%2Frds-db"} {
			if !strings.Contains(cc.Password, want) {
				t.Errorf("token %q lacks %q", cc.Password, want)
			}
		}
	}
	if cfg.Password != "" {
		t.Error("the hook changed the shared config, not the connection's copy")
	}
}

func TestIAMKeepsTheDSNsRootCert(t *testing.T) {
	t.Parallel()
	cfg := parse(t, "postgres://pcr_app@"+rdsHost+":5432/pcr?sslmode=verify-full&sslrootcert="+writeBundle(t))
	roots := cfg.TLSConfig.RootCAs
	if _, err := Configure(t.Context(), cfg, testAWS); err != nil {
		t.Fatal(err)
	}
	if cfg.TLSConfig.RootCAs != roots {
		t.Error("Configure replaced the roots the DSN named with the embedded bundle")
	}
}

func TestIAMRequiresAWSCredentials(t *testing.T) {
	t.Parallel()
	cfg := parse(t, "postgres://pcr_app@"+rdsHost+":5432/pcr?sslmode=verify-full")
	_, err := Configure(t.Context(), cfg, &aws.Config{Region: "eu-west-1"})
	if err == nil || !strings.Contains(err.Error(), "no AWS credentials") {
		t.Errorf("err=%v, want a refusal naming the missing credentials", err)
	}
}

func TestRegionFromHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ host, want string }{
		{rdsHost, "eu-west-1"},
		{"pcr.cluster-abcdefghijkl.us-west-2.rds.amazonaws.com", "us-west-2"},
		{"pcr.proxy-abcdefghijkl.eu-central-1.rds.amazonaws.com", "eu-central-1"},
		{"abcdefghijkl.eu-west-1.rds.amazonaws.com", ""},
		{"rds.amazonaws.com", ""},
		{"pcr.abcdefghijkl.eu-west-1.rds.amazonaws.com.example", ""},
		{"pcr.abcdefghijkl.eu-west-1.notrds.amazonaws.com", ""},
		{"postgres.example.internal", ""},
		{"127.0.0.1", ""},
		{"", ""},
	} {
		if got := regionFromHost(tc.host); got != tc.want {
			t.Errorf("regionFromHost(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestEmbeddedBundleHasTheRDSRoots(t *testing.T) {
	t.Parallel()
	if n := strings.Count(string(rdsCABundle), "BEGIN CERTIFICATE"); n < 50 {
		t.Errorf("embedded RDS bundle has %d certificates, want the global bundle (100+)", n)
	}
}

func writeBundle(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rds.pem")
	if err := os.WriteFile(path, rdsCABundle, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
