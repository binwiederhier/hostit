package connections

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Credential storage. The database holds ciphertext and the key lives beside
// it as a 0600 file, so a copied hostit.db (a backup, a support dump) is not a
// copied mailbox. That is a real improvement over plaintext and NOT a full
// answer -- anything that can read the database can usually read the file next
// to it. A proper answer (age, an OS keyring, a passphrase the operator
// supplies at start) is an open question in plans/260819-connections.md.

const keyFileName = "connections.key"

// NewKey generates a fresh AES-256 key.
func NewKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// LoadOrCreateKey reads the instance's credential key, generating one on first
// use. Losing this file means every connection has to be made again -- which is
// recoverable, unlike losing the apps themselves, so it is not backed up
// anywhere special.
func LoadOrCreateKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, keyFileName)
	b, err := os.ReadFile(path)
	if err == nil {
		key, decErr := base64.StdEncoding.DecodeString(string(b))
		if decErr != nil || len(key) != 32 {
			return nil, fmt.Errorf("%s is not a valid key; move it aside to start over", path)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, err := NewKey()
	if err != nil {
		return nil, err
	}
	// Published ATOMICALLY: written in full to a temporary file, then hard-linked
	// into place. Two processes starting at once would otherwise both generate a
	// key and the second write would clobber the first, making every credential
	// sealed with the first permanently unreadable.
	//
	// A link rather than a rename, because rename would overwrite the winner --
	// which is the very thing being prevented. And a link only after the content
	// is complete, so whoever loses the race never reads a half-written file.
	if err := publishKey(path, key); err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreateKey(dataDir) // somebody else won; use theirs
		}
		return nil, err
	}
	return key, nil
}

// publishKey writes the key to a temporary file beside its destination and
// links it into place, returning os.ErrExist if the destination already exists.
func publishKey(path string, key []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".connections.key-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(base64.StdEncoding.EncodeToString(key)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(tmp.Name(), path)
}

// Binding is the additional authenticated data tying a sealed credential to the
// row it belongs to. GCM authenticates the BYTES; without this it says nothing
// about WHOSE they are, so ciphertext is portable -- a bad migration, a restore
// that mixes rows, or anything that can write the database could move one
// person's sealed secret into another's connection and have it decrypt cleanly.
//
// The owner and the connection id, both of which are stable for the life of the
// row. The slug is deliberately not in it: renaming a connection must not make
// its credential unreadable.
func Binding(userID, connectionID string) []byte {
	return []byte("hostit-connection:" + userID + ":" + connectionID)
}

// Seal encrypts a credential for storage, bound to aad (see Binding). The nonce
// is random per call and prefixed, so the same credential stored twice does not
// produce equal rows.
func Seal(key []byte, plaintext string, aad []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), aad)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a stored credential, which must have been sealed with the same
// aad. A wrong key -- or the wrong row -- is an error rather than rubbish:
// rubbish would be sent to a provider as a token and fail somewhere far from
// the cause.
func Open(key []byte, sealed string, aad []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("stored credential is too short to be valid")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	out, err := gcm.Open(nil, nonce, body, aad)
	if err != nil {
		return "", fmt.Errorf("cannot decrypt the stored credential: %w", err)
	}
	return string(out), nil
}

// OpenLegacyTolerant opens a credential that may predate binding. It tries the
// bound form first and falls back to the unbound one, reporting which it was so
// the caller can re-seal it.
//
// The fallback exists because this shipped to a live instance holding real
// connections: without it, hardening the storage would mean re-authorising
// every account. It does NOT weaken the new form -- a credential sealed WITH a
// binding still refuses to open under the wrong one, which is the property the
// change is for.
func OpenLegacyTolerant(key []byte, sealed string, aad []byte) (plaintext string, bound bool, err error) {
	if out, err := Open(key, sealed, aad); err == nil {
		return out, true, nil
	}
	out, err := Open(key, sealed, nil)
	if err != nil {
		return "", false, err
	}
	return out, false, nil
}

// ReplaceKey writes a new key over the existing one. Unlike LoadOrCreateKey it
// deliberately overwrites, because rotation means replacing -- the caller has
// already re-sealed everything under the new key and the old one is now the
// thing being got rid of.
//
// The old key is kept beside it, once, as connections.key.previous: if the
// process dies between re-sealing and this write, that file is the only way
// back. Delete it once a rotation is known good.
func ReplaceKey(dataDir string, old, key []byte) error {
	path := filepath.Join(dataDir, keyFileName)
	if len(old) > 0 {
		prev := path + ".previous"
		if err := os.WriteFile(prev, []byte(base64.StdEncoding.EncodeToString(old)), 0o600); err != nil {
			return fmt.Errorf("cannot keep the previous key: %w", err)
		}
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
