package nca

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ValidateEndpoint prevents sending certificate data over plaintext remote HTTP.
// The endpoint is operator configuration and is never taken from a browser.
func ValidateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ErrUnavailable
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return ErrUnavailable
}

type Verifier struct {
	endpoint string
	client   *http.Client
	now      func() time.Time
}

func NewVerifier(endpoint string) (*Verifier, error) {
	if err := ValidateEndpoint(endpoint); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Verifier{endpoint: strings.TrimRight(endpoint, "/"), now: time.Now, client: &http.Client{
		Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type certificate struct {
	Valid       bool                                                       `json:"valid"`
	NotBefore   time.Time                                                  `json:"notBefore"`
	NotAfter    time.Time                                                  `json:"notAfter"`
	KeyUsage    string                                                     `json:"keyUsage"`
	Subject     struct{ IIN, CommonName, LastName, Email, Country string } `json:"subject"`
	Revocations []struct {
		By      string `json:"by"`
		Revoked *bool  `json:"revoked"`
	} `json:"revocations"`
}

type verification struct {
	Status  int  `json:"status"`
	Valid   bool `json:"valid"`
	Signers []struct {
		Status       string        `json:"status"`
		Certificates []certificate `json:"certificates"`
	} `json:"signers"`
}

func (v *Verifier) Verify(ctx context.Context, signature string, expected []byte) (Identity, error) {
	normalized, err := normalizeSignature(signature)
	if err != nil || len(expected) == 0 {
		return Identity{}, ErrInvalidSignature
	}
	signature = normalized
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var report verification
	if err := v.post(ctx, "/cms/verify", struct {
		CMS             string   `json:"cms"`
		RevocationCheck []string `json:"revocationCheck"`
	}{signature, []string{"OCSP"}}, &report); err != nil {
		return Identity{}, err
	}
	if report.Status != 200 || !report.Valid || len(report.Signers) != 1 {
		return Identity{}, ErrInvalidSignature
	}
	signer := report.Signers[0]
	if signer.Status != "VALID" || len(signer.Certificates) != 1 {
		return Identity{}, ErrInvalidSignature
	}
	cert := signer.Certificates[0]
	now := v.now()
	if !cert.Valid || cert.NotBefore.IsZero() || cert.NotAfter.IsZero() || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) || cert.Subject.Country != "KZ" || (cert.KeyUsage != "AUTH" && cert.KeyUsage != "SIGN") {
		return Identity{}, ErrInvalidSignature
	}
	ocsp := false
	for _, revocation := range cert.Revocations {
		if revocation.Revoked == nil || *revocation.Revoked {
			return Identity{}, ErrInvalidSignature
		}
		if revocation.By == "OCSP" {
			ocsp = true
		}
	}
	if !ocsp {
		return Identity{}, ErrInvalidSignature
	}
	name := strings.TrimSpace(cert.Subject.CommonName)
	// NCANode maps the certificate's G attribute to subject.lastName. NCA
	// certificates can keep the patronymic there rather than in CN.
	if part := strings.TrimSpace(cert.Subject.LastName); name != "" && part != "" && !strings.Contains(" "+strings.ToUpper(name)+" ", " "+strings.ToUpper(part)+" ") {
		name += " " + part
	}
	identity := Identity{IIN: cert.Subject.IIN, Name: name, Email: strings.TrimSpace(cert.Subject.Email)}
	if err := identity.Validate(); err != nil {
		return Identity{}, err
	}
	// Verification alone does not bind an attached CMS to the supplied challenge.
	// Extract the content from the exact verified CMS and compare the bytes.
	var content struct {
		Status int    `json:"status"`
		Data   string `json:"data"`
	}
	if err := v.post(ctx, "/cms/extract", struct {
		CMS string `json:"cms"`
	}{signature}, &content); err != nil {
		return Identity{}, err
	}
	actual, err := base64.StdEncoding.Strict().DecodeString(content.Data)
	if err != nil || content.Status != 200 || subtle.ConstantTimeCompare(actual, expected) != 1 {
		return Identity{}, ErrInvalidSignature
	}
	return identity, nil
}

func (v *Verifier) post(ctx context.Context, path string, body, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint+path, bytes.NewReader(data))
	if err != nil {
		return ErrUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := v.client.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 400 || response.StatusCode == 422 {
		return ErrInvalidSignature
	}
	if response.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return ErrUnavailable
	}
	if err := json.Unmarshal(data, result); err != nil {
		return ErrUnavailable
	}
	return nil
}
