package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/requestid"
)

// ncaAPIStub exercises the real HTTP adapter and account wiring. It is not a
// cryptographic verifier and is never used by application startup.
type ncaTestCMS struct {
	Data, IIN                          string
	Invalid, Offline, Revoked, Expired bool
}

func ncaSignature(t *testing.T, proof ncaTestCMS) string {
	t.Helper()
	data, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data)
}
func ncaAPIStub(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			CMS         string   `json:"cms"`
			Revocations []string `json:"revocationCheck"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(request.CMS)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		var proof ncaTestCMS
		if err := json.Unmarshal(decoded, &proof); err != nil {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if proof.Offline {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/cms/extract" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": proof.Data})
			return
		}
		if r.URL.Path != "/cms/verify" || r.Method != "POST" || len(request.Revocations) != 1 || request.Revocations[0] != "OCSP" {
			t.Error("wrong NCANode request")
			w.WriteHeader(400)
			return
		}
		notAfter := time.Now().Add(time.Hour)
		if proof.Expired {
			notAfter = time.Now().Add(-time.Hour)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "valid": !proof.Invalid, "signers": []any{map[string]any{
			"status": "VALID", "certificates": []any{map[string]any{"valid": !proof.Revoked, "notBefore": time.Now().Add(-2 * time.Hour), "notAfter": notAfter, "keyUsage": "SIGN",
				"subject":     map[string]string{"iin": proof.IIN, "commonName": "Сертификат <script>alert(1)</script>", "email": "certificate@example.test", "country": "KZ"},
				"revocations": []any{map[string]any{"by": "OCSP", "revoked": proof.Revoked}},
			}},
		}}})
	}))
	t.Cleanup(server.Close)
	return server, calls
}
func edsHandler(t *testing.T, f *authFixture) (http.Handler, *atomic.Int32) {
	t.Helper()
	nca, calls := ncaAPIStub(t)
	cfg := f.app.config
	cfg.DevLoginEnabled = false
	cfg.AppEnv = "production"
	cfg.NCANodeURL = nca.URL
	cfg.EDSOrigin = "https://dorm.example.test"
	h, err := newHandler(context.Background(), f.app.db, slog.New(slog.NewJSONHandler(f.logs, nil)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h, calls
}
func edsCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "__Host-dorm_session" {
			return cookie
		}
	}
	t.Fatal("missing EDS session cookie", w.Code)
	return nil
}
func edsChallengeHTTP(t *testing.T, h http.Handler) (*http.Cookie, string, auth.EDSChallenge) {
	t.Helper()
	page := perform(h, "GET", "/auth/login", nil, nil, "")
	if page.Code != 200 {
		t.Fatal("login page", page.Code)
	}
	cookie := edsCookie(t, page)
	token := csrf(t, page)
	response := perform(h, "POST", "/auth/eds/challenge", cookie, url.Values{"csrf_token": {token}}, "")
	if response.Code != 200 {
		t.Fatal("challenge", response.Code)
	}
	var challenge auth.EDSChallenge
	if err := json.Unmarshal(response.Body.Bytes(), &challenge); err != nil {
		t.Fatal(err)
	}
	return cookie, token, challenge
}

func TestEDSHTTPAllExistingRolesUseCertificatesAndKeepLocalIDs(t *testing.T) {
	f := newAuthFixture(t)
	h, _ := edsHandler(t, f)
	core := auth.NewService(auth.NewRepository(f.app.db), audit.NewRecorder(f.app.db), auth.Options{EDSLogin: true, SessionTTL: time.Hour})
	for i, role := range []string{"student", "registry_staff", "commandant", "administration", "system_admin"} {
		iin := fmt.Sprintf("000000000%03d", i+1)
		localID := f.credentials[role].UserID
		if err := f.service.ProvisionNCAIdentity(contextWithEDSRequest(), iin, localID, false); err != nil {
			t.Fatal(err)
		}
		anonymous, csrfToken, challenge := edsChallengeHTTP(t, h)
		if !anonymous.Secure || !anonymous.HttpOnly || anonymous.Domain != "" || anonymous.SameSite != http.SameSiteLaxMode {
			t.Fatal("unsafe production anonymous cookie")
		}
		proof := ncaSignature(t, ncaTestCMS{Data: challenge.Data, IIN: iin})
		response := perform(h, "POST", "/auth/eds/login", anonymous, url.Values{"csrf_token": {csrfToken}, "cms": {proof}, "role": {"system_admin"}, "user_id": {f.credentials["system_admin"].UserID}, "iin": {"999999999999"}}, "")
		if response.Code != 200 {
			t.Fatal("certificate login", role, response.Code)
		}
		cookie := edsCookie(t, response)
		session, err := core.Session(context.Background(), cookie.Value)
		if err != nil || session.UserID != localID {
			t.Fatal("ID changed", role, err)
		}
		if cookie.Value == anonymous.Value || !cookie.Secure || cookie.Path != "/" || cookie.Domain != "" {
			t.Fatal("session not rotated or insecure")
		}
		nav, err := core.Navigation(context.Background(), localID)
		if err != nil || len(nav.Roles) != 1 {
			t.Fatal(err)
		}
		if nav.CanReadUsers != (role == "system_admin") {
			t.Fatal("certificate data changed role", role)
		}
		page := perform(h, "GET", "/account", cookie, nil, "")
		if page.Code != 200 || !strings.Contains(page.Body.String(), iin) || !strings.Contains(page.Body.String(), "certificate@example.test") || strings.Contains(page.Body.String(), "<script>alert(1)</script>") || !strings.Contains(page.Body.String(), "&lt;script&gt;") {
			t.Fatal("certificate cabinet data or XSS", role, page.Code)
		}
		if strings.Contains(page.Header().Get("Content-Security-Policy"), "wss://127.0.0.1:13579") {
			t.Fatal("NCALayer connection allowed outside login page")
		}
		if w := perform(h, "POST", "/auth/eds/login", anonymous, url.Values{"csrf_token": {csrfToken}, "cms": {proof}}, ""); w.Code != 401 {
			t.Fatal("replayed anonymous cookie", w.Code)
		}
		if strings.Contains(f.logs.String(), iin) || strings.Contains(f.logs.String(), proof) || strings.Contains(f.logs.String(), "certificate@example.test") {
			t.Fatal("certificate or signature leaked into technical logs")
		}
	}
	page := perform(h, "GET", "/auth/login?lang=kk", nil, nil, "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "ЭЦҚ арқылы кіру") || strings.Contains(page.Body.String(), `name="password"`) || strings.Contains(page.Body.String(), `name="login"`) || !strings.Contains(page.Header().Get("Content-Security-Policy"), "wss://127.0.0.1:13579") {
		t.Fatal("production EDS-only page", page.Code)
	}
	if script := perform(h, "GET", "/static/ncalayer-login.js", nil, nil, ""); script.Code != 200 || !strings.Contains(script.Body.String(), "kz.gov.pki.knca.basics") {
		t.Fatal("login script unavailable", script.Code)
	}
	anonymous, token, _ := edsChallengeHTTP(t, h)
	dev := f.credentials["student"]
	if w := perform(h, "POST", "/auth/login", anonymous, url.Values{"csrf_token": {token}, "login": {dev.Login}, "password": {dev.Password}}, ""); w.Code != 404 {
		t.Fatal("production allowed development password", w.Code)
	}
}

func contextWithEDSRequest() context.Context {
	return requestid.WithID(context.Background(), "eds-http-test")
}

func TestEDSHTTPAutomaticRegistrationProfileAndInvalidProofs(t *testing.T) {
	f := newAuthFixture(t)
	h, calls := edsHandler(t, f)
	for _, path := range []string{"/auth/eds/challenge", "/auth/eds/login"} {
		if w := perform(h, "GET", path, nil, nil, ""); w.Code != 405 {
			t.Fatal("mutating GET", w.Code)
		}
		if w := perform(h, "POST", path, nil, nil, ""); w.Code != 401 {
			t.Fatal("anonymous missing session", w.Code)
		}
	}
	anonymous, token, challenge := edsChallengeHTTP(t, h)
	valid := ncaTestCMS{Data: challenge.Data, IIN: "000000000099"}
	proof := ncaSignature(t, valid)
	before := calls.Load()
	for _, path := range []string{"/auth/eds/challenge", "/auth/eds/login"} {
		if w := perform(h, "POST", path, anonymous, url.Values{"cms": {proof}}, ""); w.Code != 403 {
			t.Fatal("no CSRF", w.Code)
		}
		if w := perform(h, "POST", path, anonymous, url.Values{"csrf_token": {token}, "cms": {proof}}, "https://attacker.invalid"); w.Code != 403 {
			t.Fatal("cross-origin", w.Code)
		}
	}
	if before != calls.Load() {
		t.Fatal("CSRF rejection reached cryptographic service")
	}
	for _, test := range []struct {
		proof ncaTestCMS
		want  int
	}{
		{ncaTestCMS{Data: challenge.Data, IIN: valid.IIN, Invalid: true}, 401},
		{ncaTestCMS{Data: challenge.Data, IIN: valid.IIN, Offline: true}, 503},
		{ncaTestCMS{Data: challenge.Data, IIN: valid.IIN, Expired: true}, 401},
		{ncaTestCMS{Data: challenge.Data, IIN: valid.IIN, Revoked: true}, 401},
		{ncaTestCMS{Data: base64.StdEncoding.EncodeToString([]byte("different challenge")), IIN: valid.IIN}, 401},
	} {
		w := perform(h, "POST", "/auth/eds/login", anonymous, url.Values{"csrf_token": {token}, "cms": {ncaSignature(t, test.proof)}}, "")
		if w.Code != test.want {
			t.Fatal("invalid proof", w.Code, test.want)
		}
	}
	der, err := base64.StdEncoding.DecodeString(proof)
	if err != nil {
		t.Fatal(err)
	}
	pemProof := string(pem.EncodeToMemory(&pem.Block{Type: "CMS", Bytes: der}))
	w := perform(h, "POST", "/auth/eds/login", anonymous, url.Values{"csrf_token": {token}, "cms": {pemProof}, "role_id": {"system_admin"}}, "")
	if w.Code != 200 {
		t.Fatal("automatic registration", w.Code)
	}
	cookie := edsCookie(t, w)
	core := auth.NewService(auth.NewRepository(f.app.db), audit.NewRecorder(f.app.db), auth.Options{EDSLogin: true, SessionTTL: time.Hour})
	session, err := core.Session(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Authorize(context.Background(), session.UserID, "auth.users.roles.write", ""); err == nil {
		t.Fatal("new account self-selected admin role")
	}
	profile := perform(h, "GET", "/student/profile", cookie, nil, "")
	if profile.Code != 200 || !strings.Contains(profile.Body.String(), "сертификат ЭЦП НУЦ РК") || !strings.Contains(profile.Body.String(), "certificate@example.test") || !strings.Contains(profile.Body.String(), `id="student-iin" value="`+valid.IIN+`" readonly`) {
		t.Fatal("certificate student profile missing", profile.Code)
	}
	// IIN is certificate identity data, never an editable student field.
	spoof := url.Values{"csrf_token": {csrf(t, profile)}, "version": {applicationHidden(t, profile.Body.String(), "version")},
		"name": {"Имя из формы"}, "email": {"certificate@example.test"}, "group": {"Группа 1"},
		"iin": {"999999999999"}, "user_id": {f.credentials["system_admin"].UserID}}
	if w := perform(h, "POST", "/student/profile", cookie, spoof, ""); w.Code != 303 {
		t.Fatal("profile save", w.Code)
	}
	profile = perform(h, "GET", "/student/profile", cookie, nil, "")
	if profile.Code != 200 || !strings.Contains(profile.Body.String(), `id="student-iin" value="`+valid.IIN+`" readonly`) || strings.Contains(profile.Body.String(), "999999999999") {
		t.Fatal("form changed certificate IIN", profile.Code)
	}
	certificate, err := core.CertificateProfile(context.Background(), session.UserID)
	if err != nil || certificate.IIN != valid.IIN {
		t.Fatal("identity was overwritten", err)
	}
	other := secondStudent(t, f, cookie)
	otherProfile := perform(h, "GET", "/student/profile", other, nil, "")
	if otherProfile.Code != 200 || strings.Contains(otherProfile.Body.String(), valid.IIN) || !strings.Contains(otherProfile.Body.String(), `id="student-iin" value="" readonly`) {
		t.Fatal("IIN leaked into another profile", otherProfile.Code)
	}
	kkProfile := perform(h, "GET", "/student/profile?lang=kk", cookie, nil, "")
	if kkProfile.Code != 200 || !strings.Contains(kkProfile.Body.String(), "ЖСН") || !strings.Contains(kkProfile.Body.String(), valid.IIN) {
		t.Fatal("IIN not localized", kkProfile.Code)
	}
	anonymous, token, challenge = edsChallengeHTTP(t, h)
	valid.Data = challenge.Data
	w = perform(h, "POST", "/auth/eds/login", anonymous, url.Values{"csrf_token": {token}, "cms": {ncaSignature(t, valid)}}, "")
	second, err := core.Session(context.Background(), edsCookie(t, w).Value)
	if err != nil || second.UserID != session.UserID {
		t.Fatal("duplicate account", err)
	}
	var count int
	if err := f.app.db.QueryRowContext(context.Background(), `SELECT count(*) FROM auth_external_identities WHERE provider='nca' AND subject=?`, valid.IIN).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate IIN", count, err)
	}
	if w := perform(h, "POST", "/auth/eds/login", anonymous, url.Values{"csrf_token": {token}, "cms": {strings.Repeat("x", 100000)}}, ""); w.Code != 401 && w.Code != 413 {
		t.Fatal("oversize request", strconv.Itoa(w.Code))
	}
}
