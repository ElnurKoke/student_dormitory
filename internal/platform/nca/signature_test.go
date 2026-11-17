package nca

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
)

func TestSignatureEnvelopesPreserveCMSBytes(t *testing.T) {
	der := []byte("signed-test")
	canonical := base64.StdEncoding.EncodeToString(der)
	cms := string(pem.EncodeToMemory(&pem.Block{Type: "CMS", Bytes: der}))
	pkcs7 := string(pem.EncodeToMemory(&pem.Block{Type: "PKCS7", Bytes: der}))
	for _, signature := range []string{canonical, " \n" + canonical + "\r\n", "c2lnbmVk\r\nLXRlc3Q=", cms, strings.ReplaceAll(cms, "\n", "\r\n"), pkcs7} {
		got, err := normalizeSignature(signature)
		if err != nil || got != canonical {
			t.Fatal("CMS transport bytes changed", err)
		}
	}
	for _, signature := range []string{
		"", " \n", "%%%", "prefix\n" + cms, cms + "suffix", cms + cms, cms + pkcs7,
		"-----BEGIN CMS-----\ninvalid\n-----END CMS-----\n" + cms,
		"-----BEGIN CMS-----\nnot-base64\n-----END CMS-----",
		"-----BEGIN CMS-----\n" + canonical,
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "CMS", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: der})),
		strings.Repeat("a", MaxSignatureLength+1),
	} {
		if _, err := normalizeSignature(signature); err != ErrInvalidSignature {
			t.Fatal("invalid envelope accepted", err)
		}
	}
}

func TestPEMReachesNCANodeAsCanonicalBase64AndStillRequiresVerification(t *testing.T) {
	for _, trusted := range []bool{true, false} {
		t.Run(map[bool]string{true: "verified", false: "untrusted"}[trusted], func(t *testing.T) {
			v, _ := NewVerifier("http://127.0.0.1:14579")
			calls := 0
			v.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var request struct {
					CMS string `json:"cms"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.CMS != "c2lnbmVkLXRlc3Q=" {
					t.Fatal("NCANode received a PEM wrapper or changed bytes")
				}
				if r.URL.Path == "/cms/verify" {
					report := validReport()
					report["valid"] = trusted
					return jsonResponse(200, report), nil
				}
				return jsonResponse(200, map[string]any{"status": 200, "data": "Y2hhbGxlbmdl"}), nil
			})
			cms := string(pem.EncodeToMemory(&pem.Block{Type: "CMS", Bytes: []byte("signed-test")}))
			identity, err := v.Verify(context.Background(), cms, []byte("challenge"))
			if trusted && (err != nil || calls != 2 || identity.IIN != "000000000001") {
				t.Fatal("valid transport rejected", err, calls)
			}
			if !trusted && (err != ErrInvalidSignature || calls != 1 || identity.IIN != "") {
				t.Fatal("PEM bypassed trust verification", err, calls)
			}
		})
	}
}
