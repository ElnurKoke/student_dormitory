package nca

import (
	"encoding/base64"
	"encoding/pem"
	"strings"
)

// normalizeSignature accepts the CMS PEM envelope returned by NCALayer basics
// and Base64 DER used by NCANode. Only the transport envelope is changed; the
// exact CMS bytes still undergo signature, trust, revocation and content checks.
func normalizeSignature(signature string) (string, error) {
	if len(signature) == 0 || len(signature) > MaxSignatureLength {
		return "", ErrInvalidSignature
	}
	signature = strings.TrimSpace(signature)
	var der []byte
	if strings.HasPrefix(signature, "-----BEGIN ") {
		if strings.Count(signature, "-----BEGIN ") != 1 || strings.Count(signature, "-----END ") != 1 {
			return "", ErrInvalidSignature
		}
		if !strings.HasPrefix(signature, "-----BEGIN CMS-----") && !strings.HasPrefix(signature, "-----BEGIN PKCS7-----") {
			return "", ErrInvalidSignature
		}
		block, rest := pem.Decode([]byte(signature))
		if block == nil || (block.Type != "CMS" && block.Type != "PKCS7") || len(block.Headers) != 0 || strings.TrimSpace(string(rest)) != "" {
			return "", ErrInvalidSignature
		}
		der = block.Bytes
	} else {
		var err error
		der, err = base64.StdEncoding.Strict().DecodeString(signature)
		if err != nil {
			return "", ErrInvalidSignature
		}
	}
	if len(der) == 0 {
		return "", ErrInvalidSignature
	}
	return base64.StdEncoding.EncodeToString(der), nil
}
