// rds-auth-token: standalone replacement for `aws rds generate-db-auth-token`.
// Resolves AWS credentials for a profile (including SSO) and emits a SigV4
// presigned RDS IAM auth token. No host process exec; only reads ~/.aws and
// makes the SSO GetRoleCredentials network call the SDK needs.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
)

const usage = "usage: rds-auth-token --hostname H --port N --username U [--region R] [--profile P]"

func arg(args []string, i *int) string { *i++; return args[*i] }

// regionFromHost extracts the AWS region from an RDS endpoint hostname.
// RDS endpoints look like "<name>.<id>.<region>.rds.amazonaws.com", so after
// stripping the ".rds.amazonaws.com" suffix the region is the last remaining
// dot-segment. Returns "" if the host doesn't match that shape.
func regionFromHost(host string) string {
	const suffix = ".rds.amazonaws.com"
	if !strings.HasSuffix(host, suffix) {
		return ""
	}
	parts := strings.Split(strings.TrimSuffix(host, suffix), ".")
	return parts[len(parts)-1]
}

func main() {
	var profile, host, port, user, region string
	a := os.Args[1:]
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--profile":
			profile = arg(a, &i)
		case "--hostname":
			host = arg(a, &i)
		case "--port":
			port = arg(a, &i)
		case "--username", "--user":
			user = arg(a, &i)
		case "--region":
			region = arg(a, &i)
		case "-h", "--help":
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(0)
		}
	}
	// --profile is optional (matching the AWS CLI, which falls back to the
	// default credential chain); hostname/port/username are required.
	if host == "" || port == "" || user == "" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx := context.Background()
	opts := []func(*config.LoadOptions) error{}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config:", err)
		os.Exit(1)
	}
	// Region precedence: explicit --region (already applied above) wins, then
	// the profile's resolved region, then derive it from the RDS hostname so we
	// still work when a profile has no default region.
	regionToUse := cfg.Region
	if regionToUse == "" {
		regionToUse = regionFromHost(host)
	}
	if regionToUse == "" {
		fmt.Fprintln(os.Stderr, "could not determine region: pass --region or use an endpoint that embeds it")
		os.Exit(1)
	}
	tok, err := auth.BuildAuthToken(ctx, host+":"+port, regionToUse, user, cfg.Credentials)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build token:", err)
		os.Exit(1)
	}
	fmt.Println(tok)
}
