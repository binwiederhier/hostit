package server

import (
	"fmt"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/route53"
	"heckel.io/hostit/config"
)

const (
	// dnsPropagationDelay gives Route 53 a head start before certmagic starts
	// polling for the challenge record
	dnsPropagationDelay = 10 * time.Second
	// dnsPropagationTimeout bounds the wait for the record to become visible
	dnsPropagationTimeout = 5 * time.Minute
)

// dnsSolver builds the DNS-01 challenge solver for the configured provider.
// Credentials may come from the config or, when left empty, from the usual AWS
// environment variables and instance roles.
func dnsSolver(conf *config.Config) (*certmagic.DNS01Solver, error) {
	if conf.DNSProvider != config.DNSProviderRoute53 {
		return nil, fmt.Errorf("unsupported dns-provider %q", conf.DNSProvider)
	}
	provider := &route53.Provider{
		Region:          conf.AWSRegion,
		AccessKeyId:     conf.AWSAccessKeyID,
		SecretAccessKey: conf.AWSSecretKey,
		HostedZoneID:    conf.AWSHostedZoneID,
	}
	return &certmagic.DNS01Solver{
		DNSManager: certmagic.DNSManager{
			DNSProvider:        provider,
			PropagationDelay:   dnsPropagationDelay,
			PropagationTimeout: dnsPropagationTimeout,
		},
	}, nil
}
