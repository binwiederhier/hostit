package node

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Node enrollment: `hostit-control node add` mints a single-use join token; on
// the new machine, `hostit-node join` exchanges it for an mTLS certificate
// signed by control's CA. The token embeds the CA fingerprint, so the joining
// node authenticates control BEFORE sending the secret -- a rogue control
// (MITM) never sees a usable token. After the exchange the token is burned;
// all further authentication is the certificate's CN.

const (
	// JoinPath is the enrollment endpoint on control's node listener; unlike
	// everything else there, it requires no client certificate (the token is
	// the credential).
	JoinPath = "/internal/node/join"
	// joinTokenPrefix versions the token format.
	joinTokenPrefix = "hjt1."
	// joinTimeout bounds the whole exchange.
	joinTimeout = 30 * time.Second
)

// JoinStore is the slice of the registry the join handler needs.
type JoinStore interface {
	ConsumeNodeJoinToken(tokenHash string, now time.Time) (string, error)
}

type joinRequest struct {
	Token string `json:"token"`
	CSR   string `json:"csr"`
}

type joinResponse struct {
	Name string `json:"name"`
	Cert string `json:"cert"`
	CA   string `json:"ca"`
}

// MintJoinToken creates the one-time enrollment token for a node and the hash
// the registry stores; the plaintext is shown once and never persisted.
func MintJoinToken(name string, ca *CA) (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	secret := hex.EncodeToString(buf)
	payload := name + ":" + secret + ":" + ca.Fingerprint()
	return joinTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(payload)), TokenHash(secret), nil
}

// ParseJoinToken splits a token into the node name, the secret and the pinned
// CA fingerprint.
func ParseJoinToken(token string) (name, secret, caFP string, err error) {
	raw, ok := strings.CutPrefix(token, joinTokenPrefix)
	if !ok {
		return "", "", "", fmt.Errorf("not a join token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", "", "", fmt.Errorf("malformed join token: %w", err)
	}
	parts := strings.Split(string(payload), ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("malformed join token")
	}
	return parts[0], parts[1], parts[2], nil
}

// TokenHash is what the registry stores in place of the secret.
func TokenHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// JoinHandler is control's side of the exchange: burn the token, sign the
// CSR's key with the token's node name as CN, hand back cert + CA.
func JoinHandler(ca *CA, store JoinStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req joinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		tokenName, secret, _, err := ParseJoinToken(req.Token)
		if err != nil {
			http.Error(w, "invalid token", http.StatusForbidden)
			return
		}
		name, err := store.ConsumeNodeJoinToken(TokenHash(secret), time.Now())
		if err != nil || name != tokenName {
			http.Error(w, "invalid token", http.StatusForbidden)
			return
		}
		certPEM, err := ca.IssueFromCSR(name, []byte(req.CSR))
		if err != nil {
			http.Error(w, "cannot sign certificate", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&joinResponse{Name: name, Cert: certPEM, CA: ca.CertPEM()})
	})
}

// enroll enrolls this machine: dial control's node listener with the token's CA
// fingerprint as the only trust anchor, exchange token + CSR for the node's
// certificate, and persist the credentials under dataDir like the colocated
// ones -- afterwards `hostit-node serve` dials with plain mTLS.
func enroll(addr, token, dataDir string) error {
	_, _, caFP, err := ParseJoinToken(token)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: joinTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				// Trust is the token's pinned fingerprint, nothing else: the
				// chain must contain the CA it names, and the leaf must be
				// signed by it. (The CA is not in any system trust store, so
				// standard verification cannot apply here.)
				InsecureSkipVerify:    true,
				VerifyPeerCertificate: verifyPinnedCA(caFP),
				MinVersion:            tls.VersionTLS13,
			},
		},
	}
	return joinOver(client, "https://"+addr, token, dataDir)
}

// joinOver runs the exchange over a prepared client (tests use httptest's).
func joinOver(client *http.Client, baseURL, token, dataDir string) error {
	name, _, caFP, err := ParseJoinToken(token)
	if err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return err
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	body, err := json.Marshal(&joinRequest{Token: token, CSR: string(csrPEM)})
	if err != nil {
		return err
	}
	resp, err := client.Post(baseURL+JoinPath, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("join refused: %s", resp.Status)
	}
	var jr joinResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return err
	}
	if jr.Name != name {
		return fmt.Errorf("join response for wrong node %q", jr.Name)
	}
	// The returned CA must be the pinned one; anything else is tampering.
	caBlock, _ := pem.Decode([]byte(jr.CA))
	if caBlock == nil || fingerprint(caBlock.Bytes) != caFP {
		return fmt.Errorf("returned CA does not match the token's fingerprint")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	dir := filepath.Join(dataDir, ipcDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writePEMPair(dir, name, jr.Cert, string(keyPEM)); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "ca.pem"), []byte(jr.CA), 0o600)
}

// verifyPinnedCA accepts a presented chain iff it contains the CA with the
// given fingerprint and the leaf verifies against exactly that CA.
func verifyPinnedCA(caFP string) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("no certificate presented")
		}
		roots := x509.NewCertPool()
		found := false
		for _, der := range rawCerts {
			if fingerprint(der) != caFP {
				continue
			}
			ca, err := x509.ParseCertificate(der)
			if err != nil {
				return err
			}
			roots.AddCert(ca)
			found = true
		}
		if !found {
			return fmt.Errorf("no certificate in the chain matches the token's CA fingerprint")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return err
		}
		_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		return err
	}
}

// fingerprint is the SHA256 of a certificate's DER, hex-encoded.
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}
