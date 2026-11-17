package nca

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(status int, value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}
func validReport() map[string]any {
	return map[string]any{"status": 200, "valid": true, "signers": []any{map[string]any{
		"status": "VALID", "certificates": []any{map[string]any{
			"valid": true, "notBefore": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "notAfter": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"keyUsage": "SIGN", "subject": map[string]any{"iin": "000000000001", "commonName": "Тестовый Студент", "email": "test@example.test", "country": "KZ"},
			"revocations": []any{map[string]any{"by": "OCSP", "revoked": false}},
		}},
	}}}
}
func firstCertificate(report map[string]any) map[string]any {
	return report["signers"].([]any)[0].(map[string]any)["certificates"].([]any)[0].(map[string]any)
}

func TestVerifyRequiresSignatureTrustCurrentValidityAndOnlineRevocation(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func(map[string]any)
		want  error
	}{
		{"valid", func(map[string]any) {}, nil},
		{"bad_crypto_or_chain", func(r map[string]any) { r["valid"] = false }, ErrInvalidSignature},
		{"no_signers", func(r map[string]any) { r["signers"] = []any{} }, ErrInvalidSignature},
		{"several_signers", func(r map[string]any) { r["signers"] = append(r["signers"].([]any), r["signers"].([]any)[0]) }, ErrInvalidSignature},
		{"indeterminate", func(r map[string]any) { r["signers"].([]any)[0].(map[string]any)["status"] = "INDETERMINATE" }, ErrInvalidSignature},
		{"untrusted_certificate", func(r map[string]any) { firstCertificate(r)["valid"] = false }, ErrInvalidSignature},
		{"expired_despite_historical_signature", func(r map[string]any) {
			firstCertificate(r)["notAfter"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		}, ErrInvalidSignature},
		{"not_yet_valid", func(r map[string]any) {
			firstCertificate(r)["notBefore"] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
		}, ErrInvalidSignature},
		{"missing_dates", func(r map[string]any) { delete(firstCertificate(r), "notBefore") }, ErrInvalidSignature},
		{"revoked", func(r map[string]any) {
			firstCertificate(r)["revocations"] = []any{map[string]any{"by": "OCSP", "revoked": true}}
		}, ErrInvalidSignature},
		{"revocation_unknown", func(r map[string]any) {
			firstCertificate(r)["valid"] = false
			firstCertificate(r)["revocations"] = []any{map[string]any{"by": "OCSP", "revoked": false}}
		}, ErrInvalidSignature},
		{"missing_revocation", func(r map[string]any) { delete(firstCertificate(r), "revocations") }, ErrInvalidSignature},
		{"missing_revoked_value", func(r map[string]any) { firstCertificate(r)["revocations"] = []any{map[string]any{"by": "OCSP"}} }, ErrInvalidSignature},
		{"offline_crl_only", func(r map[string]any) {
			firstCertificate(r)["revocations"] = []any{map[string]any{"by": "CRL", "revoked": false}}
		}, ErrInvalidSignature},
		{"no_iin", func(r map[string]any) { delete(firstCertificate(r)["subject"].(map[string]any), "iin") }, ErrInvalidSignature},
		{"invalid_iin", func(r map[string]any) { firstCertificate(r)["subject"].(map[string]any)["iin"] = "IIN000000000001" }, ErrInvalidSignature},
		{"no_name", func(r map[string]any) { firstCertificate(r)["subject"].(map[string]any)["commonName"] = "" }, ErrInvalidSignature},
		{"invalid_email", func(r map[string]any) { firstCertificate(r)["subject"].(map[string]any)["email"] = "email\nheader" }, ErrInvalidSignature},
		{"no_email_is_allowed", func(r map[string]any) { delete(firstCertificate(r)["subject"].(map[string]any), "email") }, nil},
		{"auth_key_is_allowed", func(r map[string]any) { firstCertificate(r)["keyUsage"] = "AUTH" }, nil},
		{"unknown_key_usage", func(r map[string]any) { firstCertificate(r)["keyUsage"] = "UNKNOWN" }, ErrInvalidSignature},
		{"country", func(r map[string]any) { firstCertificate(r)["subject"].(map[string]any)["country"] = "XX" }, ErrInvalidSignature},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := validReport()
			test.alter(report)
			v, err := NewVerifier("http://127.0.0.1:14579")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			v.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
					t.Fatal("wrong request")
				}
				var request struct {
					CMS             string   `json:"cms"`
					RevocationCheck []string `json:"revocationCheck"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.CMS != "c2lnbmVkLXRlc3Q=" {
					t.Fatal("CMS changed")
				}
				if r.URL.Path == "/cms/verify" {
					if len(request.RevocationCheck) != 1 || request.RevocationCheck[0] != "OCSP" {
						t.Fatal("online revocation was not requested")
					}
					return jsonResponse(200, report), nil
				}
				if r.URL.Path != "/cms/extract" {
					t.Fatal("wrong endpoint", r.URL.Path)
				}
				return jsonResponse(200, map[string]any{"status": 200, "data": base64.StdEncoding.EncodeToString([]byte("one-time-login-challenge"))}), nil
			})
			identity, err := v.Verify(context.Background(), "c2lnbmVkLXRlc3Q=", []byte("one-time-login-challenge"))
			if !errors.Is(err, test.want) {
				t.Fatal("verification", identity, err, test.want)
			}
			if err == nil && (identity.IIN != "000000000001" || identity.Name != "Тестовый Студент" || calls != 2) {
				t.Fatal(identity, calls)
			}
		})
	}
}

func TestVerifyRejectsDifferentSignedContentAndUnavailableService(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		body    string
		extract bool
		want    error
	}{
		{"wrong_content", 200, `{"status":200,"data":"b3RoZXItY2hhbGxlbmdl"}`, true, ErrInvalidSignature},
		{"detached_content", 400, `{}`, true, ErrInvalidSignature},
		{"no_content", 200, `{"status":200}`, true, ErrInvalidSignature},
		{"service_down", 503, `private service error`, false, ErrUnavailable},
		{"invalid_signature", 400, `private parser error`, false, ErrInvalidSignature},
		{"invalid_json", 200, `not-json`, false, ErrUnavailable},
		{"oversize_response", 200, strings.Repeat("x", (1<<20)+1), false, ErrUnavailable},
		{"redirect", 302, ``, false, ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			v, _ := NewVerifier("http://127.0.0.1:14579")
			v.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if test.extract && r.URL.Path == "/cms/verify" {
					return jsonResponse(200, validReport()), nil
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Location": {"https://external.invalid"}}, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			if _, err := v.Verify(context.Background(), "c2lnbmVkLXRlc3Q=", []byte("expected")); !errors.Is(err, test.want) {
				t.Fatal(err)
			}
		})
	}
	v, _ := NewVerifier("http://127.0.0.1:14579")
	v.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New("sensitive upstream error") })
	if _, err := v.Verify(context.Background(), "c2lnbmVkLXRlc3Q=", []byte("expected")); err != ErrUnavailable {
		t.Fatal("upstream details escaped", err)
	}
	for _, signature := range []string{"", "%%%", strings.Repeat("a", MaxSignatureLength+1)} {
		if _, err := v.Verify(context.Background(), signature, []byte("expected")); err != ErrInvalidSignature {
			t.Fatal(err)
		}
	}
}

func TestEndpointAndIdentityValidation(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:14579", "http://[::1]:14579", "https://verifier.example.test"} {
		if _, err := NewVerifier(endpoint); err != nil {
			t.Fatal(endpoint, err)
		}
	}
	for _, endpoint := range []string{"", "https://user:password@example.test", "http://remote.test", "file:///tmp/nca", "https://example.test?iin=secret", "https://example.test/verify", "https://example.test#fragment"} {
		if _, err := NewVerifier(endpoint); err == nil {
			t.Fatal("unsafe endpoint", endpoint)
		}
	}
	for _, iin := range []string{"", "123", "00000000000a", "１２３４５６７８９０１２"} {
		if ValidIIN(iin) {
			t.Fatal("invalid IIN", iin)
		}
	}
}

func TestCertificateNameIncludesGAttributeWithoutDuplicate(t *testing.T) {
	for _, name := range []string{"Тестовый Студент", "Тестовый Студент Тестович"} {
		report := validReport()
		subject := firstCertificate(report)["subject"].(map[string]any)
		subject["commonName"], subject["lastName"] = name, "Тестович"
		v, _ := NewVerifier("http://127.0.0.1:14579")
		v.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/cms/verify" {
				return jsonResponse(200, report), nil
			}
			return jsonResponse(200, map[string]any{"status": 200, "data": "Y2hhbGxlbmdl"}), nil
		})
		identity, err := v.Verify(context.Background(), "c2lnbmVkLXRlc3Q=", []byte("challenge"))
		if err != nil || identity.Name != "Тестовый Студент Тестович" {
			t.Fatal(identity, err)
		}
	}
}
