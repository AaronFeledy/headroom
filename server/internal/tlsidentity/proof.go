package tlsidentity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func Fingerprint(der []byte) string { hash := sha256.Sum256(der); return hex.EncodeToString(hash[:]) }

func Proof(token, nonce, fingerprint string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("headroom-tls-proof-v1\n" + strings.ToLower(nonce) + "\n" + fingerprint))
	return hex.EncodeToString(mac.Sum(nil))
}
