package link

import (
	"crypto/tls"
	"errors"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/node/config"
)

// Cluster credentials are plain files. Each process presents a CA-signed
// certificate (CN = its member id, OU = its role: control, node or proxy) and
// trusts the cluster CA. hostit does NOT generate these -- the operator issues
// them from an external CA (see the openssl recipe in the example config / docs)
// and hands them over as cluster-cert-file / cluster-key-file /
// cluster-ca-cert-file. Possession of a valid cert IS membership; there is no
// enrollment command. A colocated member needs no certs at all: it reaches
// control over the unix socket, where the kernel peer-cred gate is the identity.

// ListenerCreds resolves control's mTLS node-listener TLS config from its
// configured cluster files. Only called when listen-cluster is set; a colocated
// install has no remote listener and needs no certs.
func ListenerCreds(certFile, keyFile, caFile string) (*tls.Config, error) {
	if certFile == "" {
		return nil, errors.New("listen-cluster needs cluster-cert-file, cluster-key-file and cluster-ca-cert-file")
	}
	cert, pool, err := cluster.LoadCreds(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	return cluster.ServerTLS(cert, pool), nil
}

// DialCreds resolves a remote node's client TLS config for dialing control's
// mTLS listener. Only called for a remote node (control-url is a host:port); a
// colocated node dials the unix socket and never reaches here.
func DialCreds(conf *config.Config) (*tls.Config, error) {
	if conf.ClusterCertFile == "" {
		return nil, errors.New("a remote node needs cluster-cert-file, cluster-key-file and cluster-ca-cert-file")
	}
	cert, pool, err := cluster.LoadCreds(conf.ClusterCertFile, conf.ClusterKeyFile, conf.ClusterCACertFile)
	if err != nil {
		return nil, err
	}
	return cluster.ClientTLS(cert, pool), nil
}
