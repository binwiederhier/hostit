package cluster

import (
	"crypto/tls"
	"crypto/x509"
)

// The cluster mTLS config builders. hostit no longer runs its own CA: the
// operator issues each member a cert from an external CA (CN = member id,
// OU = its role) and hands over cluster-cert-file / cluster-key-file /
// cluster-ca-cert-file. These turn a loaded cert + CA pool into the TLS config
// for control's accepting side and a member's dialing side.

// ServerTLS is control's node-listener config: present control's cert, and
// require + verify a CA-signed client cert (the member's identity).
func ServerTLS(cert tls.Certificate, clientCAs *x509.CertPool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientTLS is the dialing side's config: present the member cert, verify the
// server against the CA. ServerName pins the accepting identity.
func ClientTLS(cert tls.Certificate, rootCAs *x509.CertPool) *tls.Config {
	return &tls.Config{
		// Unconditional: a member has exactly one identity, so skip Go's
		// acceptable-CA filtering (which can silently send no cert at all).
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &cert, nil
		},
		RootCAs:    rootCAs,
		ServerName: ControlID,
		MinVersion: tls.VersionTLS13,
	}
}
