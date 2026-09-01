package simulate

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
)

// LoadConfig builds the AWS config from the ambient environment — the same
// credentials the CI job or shell already has. preflight never asks for
// credentials of its own.
//
// IAM is a global service, so region does not determine which region the policy
// simulator believes it is evaluating: measured (R3), calling the eu-west-1
// endpoint still evaluates aws:RequestedRegion as us-east-1. Region matters here
// only for endpoint selection; the value the simulator evaluates against comes
// from the plan and is supplied explicitly.
func LoadConfig(ctx context.Context, region string, maxAttempts int) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{
		config.WithRetryer(func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) {
				if maxAttempts > 0 {
					o.MaxAttempts = maxAttempts
				}
			})
		}),
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("loading AWS configuration: %w", err)
	}
	// IAM and STS are reachable from any region; default rather than fail so a
	// user with no region configured still gets a working run.
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return cfg, nil
}
