// Package rdsiam logs pcr-server in to its RDS database with IAM tokens.
//
// A DSN whose host is an RDS endpoint and that carries no password logs in as
// its user with a fresh RDS IAM token on every new connection, signed with AWS
// credentials from the default credential chain, which must allow
// rds-db:connect as that user. Any other DSN logs in as written, so local
// Postgres is unaffected. A token is valid for 15 minutes and checked only at
// login; open connections outlive it.
//
// A token is a bearer credential for its lifetime, so IAM mode requires
// sslmode=verify-full and, unless the DSN names an sslrootcert, verifies the
// server against the RDS CA bundle embedded here: the system store doesn't
// carry the RDS roots.
package rdsiam

import (
	"context"
	"crypto/x509"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/jackc/pgx/v5"
)

// rdsCABundle is https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem.
//
//go:embed rds-global-bundle.pem
var rdsCABundle []byte

// BeforeConnect sets a connection's password just before it logs in.
type BeforeConnect func(context.Context, *pgx.ConnConfig) error

// Configure prepares cfg for an IAM login when its DSN asks for one (an RDS
// host and no password) and returns the hook that sets the token on every new
// connection. For any other DSN it returns nil and leaves cfg alone. awsCfg
// overrides the default AWS config (tests); nil loads the default credential
// chain.
func Configure(ctx context.Context, cfg *pgx.ConnConfig, awsCfg *aws.Config) (BeforeConnect, error) {
	region := regionFromHost(cfg.Host)
	if region == "" || cfg.Password != "" {
		return nil, nil
	}
	if cfg.User == "" {
		return nil, errors.New("RDS IAM login: the DSN names no user; set it to a database user granted rds_iam")
	}
	if cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || len(cfg.Fallbacks) > 0 {
		return nil, errors.New("RDS IAM login: the DSN must use sslmode=verify-full, so a token is only ever sent to a verified server")
	}
	if cfg.TLSConfig.RootCAs == nil {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(rdsCABundle) {
			return nil, errors.New("RDS IAM login: the embedded RDS CA bundle has no certificates")
		}
		cfg.TLSConfig.RootCAs = roots
	}
	if awsCfg == nil {
		loaded, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("RDS IAM login: loading AWS config: %w", err)
		}
		awsCfg = &loaded
	}
	if awsCfg.Credentials == nil {
		return nil, errors.New("RDS IAM login: no AWS credentials in the default credential chain")
	}
	creds := awsCfg.Credentials
	return func(ctx context.Context, cc *pgx.ConnConfig) error {
		endpoint := net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port)))
		token, err := auth.BuildAuthToken(ctx, endpoint, region, cc.User, creds)
		if err != nil {
			return fmt.Errorf("RDS IAM login: building a token for %s@%s: %w", cc.User, endpoint, err)
		}
		cc.Password = token
		return nil
	}, nil
}

// regionFromHost reads the region from an RDS endpoint
// (<name>.<id>.<region>.rds.amazonaws.com); "" for any other host.
func regionFromHost(host string) string {
	if !strings.HasSuffix(host, ".rds.amazonaws.com") {
		return ""
	}
	parts := strings.Split(host, ".")
	if len(parts) < 6 {
		return ""
	}
	return parts[len(parts)-4]
}
