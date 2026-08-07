package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"heckel.io/hostit/store"
)

const (
	// domainIssueTimeout bounds one certificate attempt for a custom domain (a
	// DNS-01 order plus propagation)
	domainIssueTimeout = 6 * time.Minute
)

// ErrInvalidDomain is returned when a custom domain is not a usable hostname
var ErrInvalidDomain = errors.New("invalid domain")

// hostnameRegex matches a plausible public hostname (labels, at least one dot).
// It is deliberately strict: no wildcards, no trailing dot, no bare TLD.
var hostnameRegex = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// AddAppDomain attaches a custom domain to an app: it records it as pending and
// kicks off certificate issuance in the background. It returns the stored record;
// the caller renders the DNS records the owner must create (see DomainDNSRecords).
func (s *Server) AddAppDomain(appName, domain string) (*store.Domain, error) {
	domain = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(domain, ".")))
	if err := s.validateCustomDomain(domain); err != nil {
		return nil, err
	}
	d := &store.Domain{Domain: domain, AppName: appName, Status: store.DomainPending, CreatedAt: time.Now()}
	if err := s.apps.Store().AddDomain(d); err != nil {
		return nil, err
	}
	s.reloadDomains()
	go s.issueDomainCert(domain)
	return d, nil
}

// VerifyAppDomain re-attempts issuance for a domain (after the owner has set up
// DNS), moving it back to pending and trying again.
func (s *Server) VerifyAppDomain(appName, domain string) error {
	d, err := s.ownedDomain(appName, domain)
	if err != nil {
		return err
	}
	go s.issueDomainCert(d.Domain)
	return nil
}

// RemoveAppDomain detaches a custom domain: it stops routing at once. The
// certificate is left in certmagic storage to expire on its own (no revoke).
func (s *Server) RemoveAppDomain(appName, domain string) error {
	d, err := s.ownedDomain(appName, domain)
	if err != nil {
		return err
	}
	if err := s.apps.Store().DeleteDomain(d.Domain); err != nil {
		return err
	}
	s.reloadDomains()
	return nil
}

// AppDomains lists an app's custom domains.
func (s *Server) AppDomains(appName string) ([]*store.Domain, error) {
	return s.apps.Store().Domains(appName)
}

// ownedDomain fetches a domain and confirms it belongs to the given app.
func (s *Server) ownedDomain(appName, domain string) (*store.Domain, error) {
	domain = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(domain, ".")))
	d, err := s.apps.Store().Domain(domain)
	if err != nil {
		return nil, err
	}
	if d.AppName != appName {
		return nil, store.ErrAppDomainNotFound
	}
	return d, nil
}

// issueDomainCert obtains (or renews) a certificate for a domain and records the
// result. With TLS off there is no certificate to get, so the domain simply
// becomes active and routes over plain HTTP.
func (s *Server) issueDomainCert(domain string) {
	if s.magic == nil {
		s.markDomain(domain, store.DomainActive, "")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainIssueTimeout)
	defer cancel()
	if err := s.magic.ManageSync(ctx, []string{domain}); err != nil {
		slog.Warn("Custom domain certificate failed", "domain", domain, "error", err)
		s.markDomain(domain, store.DomainError, err.Error())
		return
	}
	slog.Info("Custom domain active", "domain", domain)
	s.markDomain(domain, store.DomainActive, "")
}

// markDomain records a domain's new status and refreshes the routing cache.
func (s *Server) markDomain(domain string, status store.DomainStatus, lastErr string) {
	var activeAt *time.Time
	if status == store.DomainActive {
		now := time.Now()
		activeAt = &now
	}
	if err := s.apps.Store().SetDomainStatus(domain, status, lastErr, activeAt); err != nil {
		slog.Warn("Cannot record custom domain status", "domain", domain, "error", err)
	}
	s.reloadDomains()
}

// manageExistingDomains obtains certificates for the active domains already in the
// store, so a restart serves them right away and renews them. Runs in the
// background so startup is not blocked on ACME.
func (s *Server) manageExistingDomains() {
	domains, err := s.apps.Store().AllDomains()
	if err != nil {
		slog.Warn("Cannot load custom domains at startup", "error", err)
		return
	}
	for _, d := range domains {
		if d.Status == store.DomainActive {
			go s.issueDomainCert(d.Domain)
		}
	}
}

// appNameFromCustomDomain resolves an active custom domain to its app.
func (s *Server) appNameFromCustomDomain(host string) (string, bool) {
	s.domainMu.RLock()
	loaded := s.domainCache != nil
	if loaded {
		name, ok := s.domainCache[host]
		s.domainMu.RUnlock()
		return name, ok
	}
	s.domainMu.RUnlock()
	s.reloadDomains()
	s.domainMu.RLock()
	defer s.domainMu.RUnlock()
	name, ok := s.domainCache[host]
	return name, ok
}

// reloadDomains rebuilds the active-domain routing cache from the store.
func (s *Server) reloadDomains() {
	all, err := s.apps.Store().AllDomains()
	if err != nil {
		slog.Warn("Cannot load custom domains", "error", err)
		return
	}
	cache := make(map[string]string, len(all))
	for _, d := range all {
		if d.Status == store.DomainActive {
			cache[d.Domain] = d.AppName
		}
	}
	s.domainMu.Lock()
	s.domainCache = cache
	s.domainMu.Unlock()
}

// validateCustomDomain rejects malformed hostnames and names hostit already owns
// (the base domain, web hostnames and <app>.<base> subdomains are handled without
// custom domains).
func (s *Server) validateCustomDomain(domain string) error {
	if !hostnameRegex.MatchString(domain) {
		return fmt.Errorf("%w: %q is not a valid hostname", ErrInvalidDomain, domain)
	}
	if s.config.IsWebHostname(domain) || domain == s.config.BaseDomain {
		return fmt.Errorf("%w: %q is the platform's own hostname", ErrInvalidDomain, domain)
	}
	if _, ok := s.appNameFromHost(domain); ok {
		return fmt.Errorf("%w: %q is already an app subdomain", ErrInvalidDomain, domain)
	}
	if strings.HasSuffix(domain, "."+s.config.BaseDomain) {
		return fmt.Errorf("%w: %q is under the platform domain", ErrInvalidDomain, domain)
	}
	return nil
}

// DomainDNSRecords returns the two DNS records an owner must create for a custom
// domain: one to route traffic at the app, one to delegate the ACME challenge to
// the zone we control (so DNS-01 works even when the box is not publicly reachable).
func (s *Server) DomainDNSRecords(appName, domain string) (traffic, delegation dnsRecord) {
	traffic = dnsRecord{
		Type:  "CNAME",
		Name:  domain,
		Value: appName + "." + s.config.BaseDomain,
		Note:  "Point traffic at the app (use an A/ALIAS record to the server IP at a zone apex, where CNAME is not allowed).",
	}
	delegation = dnsRecord{
		Type:  "CNAME",
		Name:  "_acme-challenge." + domain,
		Value: domain + ".acme." + s.config.BaseDomain,
		Note:  "Delegate the TLS challenge so a certificate can be issued without the server being publicly reachable.",
	}
	return traffic, delegation
}

// dnsRecord is one DNS record the owner has to create, for the API and UI.
type dnsRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
	Note  string `json:"note"`
}
