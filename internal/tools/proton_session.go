package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	proton "github.com/ProtonMail/go-proton-api"
	pgpcrypto "github.com/ProtonMail/gopenpgp/v2/crypto"
)

// Identity sent to the Proton API (x-pm-appversion). Proton only accepts, for a
// third-party client, the documented "external-drive-<project>@<semver>" shape
// (the one rclone uses: external-drive-rclone@1.0.0); the "external-calendar…" /
// "external-mail…" variants are refused with Code=5002. The project part is the
// Hub's own name: we never impersonate an official Proton app. Validated against
// the live API on 2026-10-09 (unauthenticated GET /domains/available: HTTP 200).
const (
	protonDefaultAppVersion = "external-drive-claude_hub@1.0.0"
	protonAppVersionEnv     = "HUB_PROTON_APP_VERSION" // process-wide override
	pkAppVersion            = "app_version"            // per-tool override (advanced field)
)

// platform-product[-project]@major.minor.patch[-channel[.n]][+build]
var protonAppVersionRE = regexp.MustCompile(`^[a-z]+(-[a-z_]+)+@\d+\.\d+\.\d+(-[a-z]+(\.\d+)*)?(\+[A-Za-z0-9._-]+)?$`)

// protonAppVersionOf resolves the identity: tool setting, then environment, then
// the validated default. A malformed override is an error, not a silent fallback.
func protonAppVersionOf(cfg map[string]string) (string, error) {
	v := strings.TrimSpace(cfg[pkAppVersion])
	if v == "" {
		v = strings.TrimSpace(os.Getenv(protonAppVersionEnv))
	}
	if v == "" {
		return protonDefaultAppVersion, nil
	}
	if !protonAppVersionRE.MatchString(v) {
		return "", fmt.Errorf("identité d'application Proton invalide (%q) : format attendu external-drive-<projet>@x.y.z, ex. %s", v, protonDefaultAppVersion)
	}
	return v, nil
}

var (
	protonHostURL   = "https://mail.proton.me/api"
	protonTransport http.RoundTripper // tests only
)

// Persisted (encrypted) session state, kept in the tool config next to the
// "username" field. No password is ever stored: the login happens once, from the
// Hub UI, and the session is then only refreshed.
const (
	pkUID        = "uid"
	pkRefresh    = "refresh_token"
	pkKeyPass    = "salted_key_pass" // base64 of the salted key passphrase
	pkPauseUntil = "_pause_until"    // RFC3339: no API call before this time
	pkPauseCount = "_pause_count"    // consecutive auth/anti-abuse failures
	pkLastError  = "_last_error"
)

var protonPauseSteps = []time.Duration{15 * time.Minute, time.Hour, 4 * time.Hour, 12 * time.Hour, 24 * time.Hour}

// protonPauseDelay is the growing pause after the n-th consecutive failure (n >= 1).
func protonPauseDelay(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > len(protonPauseSteps) {
		n = len(protonPauseSteps)
	}
	return protonPauseSteps[n-1]
}

func newProtonManager(appVersion string) *proton.Manager {
	opts := []proton.Option{
		proton.WithHostURL(protonHostURL),
		proton.WithAppVersion(appVersion),
		proton.WithRetryCount(1),
	}
	if protonTransport != nil {
		opts = append(opts, proton.WithTransport(protonTransport))
	}
	return proton.New(opts...)
}

// ---- error classification ----

type protonErrKind int

const (
	protonErrOther       protonErrKind = iota
	protonErrTransient                 // network / 5xx: try again later, no pause
	protonErrAbuse                     // 2028 and neighbours, 429: back off
	protonErrHuman                     // human verification (captcha) required
	protonErrSessionDead               // refresh token rejected: reconnect needed
	protonErrAppVersion                // API refuses our app identity
	protonErrBadCreds                  // wrong password / 2FA code
)

// Code=5002 "Invalid app version": the lib only names 5001 and 5003.
const protonAppVersionInvalidCode proton.Code = 5002

func protonClassify(err error) protonErrKind {
	if err == nil {
		return protonErrOther
	}
	var apiErr *proton.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == proton.HumanVerificationRequired:
			return protonErrHuman
		case apiErr.Code >= 2026 && apiErr.Code <= 2029, apiErr.Status == http.StatusTooManyRequests:
			return protonErrAbuse
		case apiErr.Code == proton.AuthRefreshTokenInvalid:
			return protonErrSessionDead
		case apiErr.Code == protonAppVersionInvalidCode, apiErr.Code == proton.AppVersionBadCode, apiErr.Code == proton.AppVersionMissingCode:
			return protonErrAppVersion
		case apiErr.Code == proton.PasswordWrong:
			return protonErrBadCreds
		case apiErr.Status == http.StatusUnauthorized:
			return protonErrSessionDead
		case apiErr.Status >= 500:
			return protonErrTransient
		}
		return protonErrOther
	}
	var netErr *proton.NetError
	if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
		return protonErrTransient
	}
	return protonErrOther
}

const (
	protonMsgReconnect  = "Connectez le compte Proton dans le Hub : Outils › ProtonMail › « Connecter le compte » (mot de passe, puis code 2FA si activé)."
	protonMsgAppVersion = "Proton refuse l'identité d'application du Hub (« %s », code %d). Ce n'est pas un blocage du compte et aucune pause n'est appliquée : corrigez le champ avancé « Identité d'application » de l'outil Proton (ou la variable d'environnement " + protonAppVersionEnv + ") avec une valeur au format external-drive-<projet>@x.y.z (défaut : " + protonDefaultAppVersion + "), enregistrez puis réessayez."
	protonMsgPaused     = "Le connecteur Proton est en pause jusqu'à %s (Proton a refusé la session : %s). Ne réessaie pas avant ; dis à l'utilisateur de vérifier son compte Proton (https://account.proton.me, ou https://proton.me/support/appeal-abuse en cas de blocage anti-abus) puis de reconnecter le compte dans le Hub."
)

// ---- state in the tool config ----

func protonPausedUntil(cfg map[string]string, now time.Time) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, cfg[pkPauseUntil])
	if err != nil || !now.Before(t) {
		return time.Time{}, false
	}
	return t, true
}

func protonPauseError(cfg map[string]string, until time.Time) error {
	reason := cfg[pkLastError]
	if reason == "" {
		reason = "erreur d'authentification"
	}
	return fmt.Errorf(protonMsgPaused, until.In(protonLocation()).Format("2006-01-02 15:04 MST"), reason)
}

// protonRecordFailure turns an upstream error into the message returned to the
// assistant, and applies the matching policy: a growing pause for anti-abuse /
// auth refusals, a forced reconnect for a dead session, nothing for transient
// failures. It never triggers a retry.
func protonRecordFailure(toolID string, cfg map[string]string, err error, now time.Time) error {
	kind := protonClassify(err)
	switch kind {
	case protonErrTransient:
		return fmt.Errorf("Proton injoignable ou en erreur temporaire, réessaie plus tard : %w", err)
	case protonErrAppVersion:
		// Our own misconfiguration, not the account's fault: report it, no pause.
		msg := protonAppVersionMessage(cfg, err)
		_ = updateToolConfig(toolID, func(c map[string]string) { c[pkLastError] = msg })
		cfg[pkLastError] = msg
		return errors.New(msg)
	case protonErrAbuse, protonErrHuman, protonErrBadCreds:
		n, _ := strconv.Atoi(cfg[pkPauseCount])
		n++
		until := now.Add(protonPauseDelay(n))
		reason := err.Error()
		_ = updateToolConfig(toolID, func(c map[string]string) {
			c[pkPauseCount] = strconv.Itoa(n)
			c[pkPauseUntil] = until.UTC().Format(time.RFC3339)
			c[pkLastError] = reason
		})
		cfg[pkPauseCount], cfg[pkPauseUntil], cfg[pkLastError] = strconv.Itoa(n), until.UTC().Format(time.RFC3339), reason
		return protonPauseError(cfg, until)
	case protonErrSessionDead:
		protonForgetSession(toolID)
		_ = updateToolConfig(toolID, func(c map[string]string) {
			delete(c, pkUID)
			delete(c, pkRefresh)
			c[pkLastError] = err.Error()
		})
		return fmt.Errorf("La session Proton a expiré ou a été révoquée (%v). %s", err, protonMsgReconnect)
	}
	return fmt.Errorf("proton: %w", err)
}

// protonAppVersionMessage explains a 5001/5002/5003 refusal.
func protonAppVersionMessage(cfg map[string]string, err error) string {
	v, verr := protonAppVersionOf(cfg)
	if verr != nil {
		return verr.Error()
	}
	var apiErr *proton.APIError
	code := 0
	if errors.As(err, &apiErr) {
		code = int(apiErr.Code)
	}
	return fmt.Sprintf(protonMsgAppVersion, v, code)
}

func protonClearPause(toolID string) {
	_ = updateToolConfig(toolID, func(c map[string]string) {
		delete(c, pkPauseUntil)
		delete(c, pkPauseCount)
		delete(c, pkLastError)
	})
}

func protonPersistAuth(toolID string, uid, refresh string) {
	_ = updateToolConfig(toolID, func(c map[string]string) {
		c[pkUID], c[pkRefresh] = uid, refresh
	})
}

// ProtonStatus is what the UI shows about the stored session.
type ProtonStatus struct {
	Connected   bool       `json:"connected"`
	Username    string     `json:"username"`
	PausedUntil *time.Time `json:"pausedUntil,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
}

func ProtonStatusOf(cfg map[string]string) ProtonStatus {
	st := ProtonStatus{
		Connected: cfg[pkUID] != "" && cfg[pkRefresh] != "" && cfg[pkKeyPass] != "",
		Username:  cfg["username"],
		LastError: cfg[pkLastError],
	}
	if until, ok := protonPausedUntil(cfg, time.Now()); ok {
		st.PausedUntil = &until
	}
	return st
}

// ---- live sessions ----

type protonSession struct {
	manager *proton.Manager
	client  *proton.Client
	userKR  *pgpcrypto.KeyRing
	addrKRs map[string]*pgpcrypto.KeyRing // lower-cased email -> key ring

	authMu sync.Mutex // current uid / access token, needed by the raw write calls (proton_write.go)
	uid    string
	access string
}

func (s *protonSession) setAuth(uid, access string) {
	s.authMu.Lock()
	s.uid, s.access = uid, access
	s.authMu.Unlock()
}

func (s *protonSession) getAuth() (uid, access string) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	return s.uid, s.access
}

var (
	protonSessions   = map[string]*protonSession{}
	protonSessionsMu sync.Mutex // also held across a refresh: the refresh token rotates, only one may use it
)

func protonForgetSession(toolID string) {
	protonSessionsMu.Lock()
	old := protonSessions[toolID]
	delete(protonSessions, toolID)
	protonSessionsMu.Unlock()
	if old != nil {
		old.manager.Close()
	}
}

// protonGetSession returns the live session for the tool, resuming it from the
// stored UID + refresh token when needed. It never performs a password login.
func protonGetSession(ctx context.Context, cfg map[string]string, toolID string) (*protonSession, error) {
	if toolID == "" {
		return nil, errors.New("outil Proton non enregistré")
	}
	now := time.Now()
	if until, paused := protonPausedUntil(cfg, now); paused {
		return nil, protonPauseError(cfg, until)
	}
	protonSessionsMu.Lock()
	defer protonSessionsMu.Unlock()
	if s := protonSessions[toolID]; s != nil {
		return s, nil
	}
	st := ProtonStatusOf(cfg)
	if !st.Connected {
		return nil, errors.New("Le compte Proton n'est pas connecté. " + protonMsgReconnect)
	}
	keyPass, err := base64.StdEncoding.DecodeString(cfg[pkKeyPass])
	if err != nil {
		return nil, errors.New("Session Proton illisible. " + protonMsgReconnect)
	}

	appVersion, err := protonAppVersionOf(cfg)
	if err != nil {
		return nil, err
	}
	m := newProtonManager(appVersion)
	client, auth, err := m.NewClientWithRefresh(ctx, cfg[pkUID], cfg[pkRefresh])
	if err != nil {
		m.Close()
		return nil, protonRecordFailure(toolID, cfg, err, now)
	}
	protonPersistAuth(toolID, auth.UID, auth.RefreshToken)
	sess, err := protonOpenSession(ctx, toolID, m, client, auth, keyPass)
	if err != nil {
		m.Close()
		return nil, protonRecordFailure(toolID, cfg, err, now)
	}
	protonSessions[toolID] = sess
	return sess, nil
}

// protonOpenSession unlocks the account keys on an authenticated client and
// hooks token rotation / revocation to the persisted state.
func protonOpenSession(ctx context.Context, toolID string, m *proton.Manager, client *proton.Client, auth proton.Auth, keyPass []byte) (*protonSession, error) {
	user, err := client.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	if len(user.Keys) == 0 {
		return nil, errors.New("le compte n'a pas de clé privée")
	}
	userKR, err := user.Keys.Unlock(keyPass, nil)
	if err != nil {
		return nil, fmt.Errorf("déverrouillage des clés: %w", err)
	}
	addrs, err := client.GetAddresses(ctx)
	if err != nil {
		return nil, err
	}
	addrKRs := map[string]*pgpcrypto.KeyRing{}
	for _, a := range addrs {
		if kr, kerr := a.Keys.Unlock(keyPass, userKR); kerr == nil {
			addrKRs[strings.ToLower(a.Email)] = kr
		}
	}
	if len(addrKRs) == 0 {
		return nil, errors.New("aucune clé d'adresse déverrouillable")
	}

	sess := &protonSession{manager: m, client: client, userKR: userKR, addrKRs: addrKRs}
	sess.setAuth(auth.UID, auth.AccessToken)
	client.AddAuthHandler(func(a proton.Auth) {
		sess.setAuth(a.UID, a.AccessToken)
		protonPersistAuth(toolID, a.UID, a.RefreshToken)
	})
	client.AddDeauthHandler(func() {
		// Called from inside a request: drop the session asynchronously.
		go func() {
			protonForgetSession(toolID)
			_ = updateToolConfig(toolID, func(c map[string]string) {
				delete(c, pkUID)
				delete(c, pkRefresh)
				c[pkLastError] = "session révoquée par Proton"
			})
		}()
	})
	return sess, nil
}

// ---- one-time connection (from the Hub UI) ----

// ProtonConnectInput carries what the user typed in the connect dialog. Nothing
// here is persisted: only the resulting session is.
type ProtonConnectInput struct {
	Username        string
	Password        string
	MailboxPassword string
	TOTP            string
	CaptchaDone     bool // the user solved the CAPTCHA: resend the auth with the HV token
}

// ProtonConnectResult tells the UI what to ask next.
type ProtonConnectResult struct {
	Status    string     `json:"status"` // connected | needs_captcha | needs_2fa | needs_mailbox_password
	Message   string     `json:"message,omitempty"`
	URL       string     `json:"url,omitempty"`       // needs_captcha: https://verify.proton.me link
	ExpiresAt *time.Time `json:"expiresAt,omitempty"` // needs_captcha: the wait ends then
}

type protonPending struct {
	username  string
	password  []byte
	mailbox   []byte
	manager   *proton.Manager
	client    *proton.Client
	auth      proton.Auth
	twoFADone bool
	attempts  int
	timer     *time.Timer
}

var (
	protonPendings   = map[string]*protonPending{}
	protonPendingsMu sync.Mutex
)

const protonPendingTTL = 5 * time.Minute

func (p *protonPending) discard(revoke bool) {
	if p.timer != nil {
		p.timer.Stop()
	}
	if revoke {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = p.client.AuthDelete(ctx)
		cancel()
	}
	p.manager.Close()
	for i := range p.password {
		p.password[i] = 0
	}
	for i := range p.mailbox {
		p.mailbox[i] = 0
	}
}

func protonDropPending(toolID string, revoke bool) {
	protonPendingsMu.Lock()
	p := protonPendings[toolID]
	delete(protonPendings, toolID)
	protonPendingsMu.Unlock()
	if p != nil {
		p.discard(revoke)
	}
}

// ProtonConnect performs the single interactive login of the account and stores
// the resulting session. It can be called several times for one connection: the
// first call carries the password, later calls the 2FA code / mailbox password
// the previous result asked for. It makes at most one password login and never
// loops: any refusal is returned as is (and anti-abuse refusals start a pause).
func ProtonConnect(ctx context.Context, toolID string, cfg map[string]string, in ProtonConnectInput) (ProtonConnectResult, error) {
	protonPendingsMu.Lock()
	p := protonPendings[toolID]
	protonPendingsMu.Unlock()

	if p == nil {
		// A CAPTCHA wait (Code=9001) is open for this tool: only the explicit
		// confirmation sends the auth again, once, with the verified token.
		if hv := protonHVPeek(toolID); hv != nil && !in.CaptchaDone {
			return hv.result(), nil
		}
		var hv *protonHV
		if in.CaptchaDone {
			if hv = protonHVTake(toolID); hv == nil {
				return ProtonConnectResult{}, errors.New("la vérification humaine a expiré (10 minutes) ou a été annulée : recommencez la connexion")
			}
		}
		username := strings.TrimSpace(in.Username)
		password := in.Password
		if hv != nil {
			username, password = hv.username, string(hv.password)
		} else if username == "" {
			username = cfg["username"]
		}
		if username == "" || password == "" {
			return ProtonConnectResult{}, errors.New("nom d'utilisateur et mot de passe requis")
		}
		now := time.Now()
		// The pause is a refusal by Proton: no auth request before its end.
		if until, paused := protonPausedUntil(cfg, now); paused {
			hv.discard()
			return ProtonConnectResult{}, protonPauseError(cfg, until)
		}
		appVersion, err := protonAppVersionOf(cfg)
		if err != nil {
			hv.discard()
			return ProtonConnectResult{}, err
		}
		m := newProtonManager(appVersion)
		var token *proton.APIHVDetails
		if hv != nil {
			token = &proton.APIHVDetails{Methods: []string{"captcha"}, Token: hv.token}
		}
		client, auth, err := m.NewClientWithLoginWithHVToken(ctx, username, []byte(password), token)
		if err != nil {
			m.Close()
			if hv == nil && protonClassify(err) == protonErrHuman {
				return protonStartCaptcha(toolID, username, in.Password, err)
			}
			hv.discard() // a second 9001 after the CAPTCHA is a real refusal: protonConnectError pauses
			return ProtonConnectResult{}, protonConnectError(toolID, cfg, err, now)
		}
		hv.discard()
		p = &protonPending{username: username, password: []byte(password), manager: m, client: client, auth: auth}
		p.timer = time.AfterFunc(protonPendingTTL, func() { protonDropPending(toolID, true) })
		protonPendingsMu.Lock()
		protonPendings[toolID] = p
		protonPendingsMu.Unlock()
	}

	// 2FA first: the session is limited until it is validated.
	if p.auth.TwoFA.Enabled != 0 && !p.twoFADone {
		if p.auth.TwoFA.Enabled == proton.HasFIDO2 {
			protonDropPending(toolID, true)
			return ProtonConnectResult{}, errors.New("ce compte n'a qu'une clé de sécurité FIDO2 comme 2FA, non gérée : ajoutez une application TOTP dans les paramètres de sécurité Proton")
		}
		code := strings.ReplaceAll(strings.TrimSpace(in.TOTP), " ", "")
		if code == "" {
			return ProtonConnectResult{Status: "needs_2fa", Message: "Saisissez le code à 6 chiffres de votre application d'authentification."}, nil
		}
		if err := p.client.Auth2FA(ctx, proton.Auth2FAReq{TwoFactorCode: code}); err != nil {
			p.attempts++
			if p.attempts >= 3 || protonClassify(err) != protonErrBadCreds {
				protonDropPending(toolID, true)
				return ProtonConnectResult{}, protonConnectError(toolID, cfg, err, time.Now())
			}
			return ProtonConnectResult{Status: "needs_2fa", Message: "Code refusé, réessayez avec le code suivant."}, nil
		}
		p.twoFADone = true
	}

	keyPass := p.password
	if p.auth.PasswordMode == proton.TwoPasswordMode {
		if in.MailboxPassword != "" {
			p.mailbox = []byte(in.MailboxPassword)
		}
		if len(p.mailbox) == 0 {
			return ProtonConnectResult{Status: "needs_mailbox_password", Message: "Ce compte est en mode « deux mots de passe » : saisissez le mot de passe de la boîte."}, nil
		}
		keyPass = p.mailbox
	}

	saltedPass, err := protonSaltedPass(ctx, p.client, keyPass)
	if err != nil {
		protonDropPending(toolID, true)
		return ProtonConnectResult{}, protonConnectError(toolID, cfg, err, time.Now())
	}
	sess, err := protonOpenSession(ctx, toolID, p.manager, p.client, p.auth, saltedPass)
	if err != nil {
		protonDropPending(toolID, true)
		return ProtonConnectResult{}, protonConnectError(toolID, cfg, err, time.Now())
	}

	// Success: keep this very session (no second login), persist it, forget secrets.
	protonPendingsMu.Lock()
	delete(protonPendings, toolID)
	protonPendingsMu.Unlock()
	p.timer.Stop()
	protonSessionsMu.Lock()
	old := protonSessions[toolID]
	protonSessions[toolID] = sess
	protonSessionsMu.Unlock()
	if old != nil {
		old.manager.Close()
	}
	username, uid, refresh, kp := p.username, p.auth.UID, p.auth.RefreshToken, base64.StdEncoding.EncodeToString(saltedPass)
	for i := range p.password {
		p.password[i] = 0
	}
	for i := range p.mailbox {
		p.mailbox[i] = 0
	}
	if err := updateToolConfig(toolID, func(c map[string]string) {
		c["username"], c[pkUID], c[pkRefresh], c[pkKeyPass] = username, uid, refresh, kp
		delete(c, "password") // legacy fields of the former password-login connector
		delete(c, "mailbox_password")
		delete(c, pkPauseUntil)
		delete(c, pkPauseCount)
		delete(c, pkLastError)
	}); err != nil {
		return ProtonConnectResult{}, fmt.Errorf("session ouverte mais non enregistrée: %w", err)
	}
	return ProtonConnectResult{Status: "connected"}, nil
}

func protonSaltedPass(ctx context.Context, client *proton.Client, keyPass []byte) ([]byte, error) {
	user, err := client.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	if len(user.Keys) == 0 {
		return nil, errors.New("le compte n'a pas de clé privée")
	}
	salts, err := client.GetSalts(ctx)
	if err != nil {
		return nil, err
	}
	return salts.SaltForKey(keyPass, user.Keys[0].ID)
}

// protonConnectError renders a login refusal for the UI; it never retries.
func protonConnectError(toolID string, cfg map[string]string, err error, now time.Time) error {
	switch protonClassify(err) {
	case protonErrHuman:
		_ = protonRecordFailure(toolID, cfg, err, now)
		return errors.New("Proton demande encore une vérification humaine (captcha) malgré le jeton, ou la refuse. Le connecteur est mis en pause (voir l'heure indiquée) : n'insistez pas avant, puis recommencez la connexion. Aucune nouvelle tentative automatique n'est faite.")
	case protonErrAbuse:
		_ = protonRecordFailure(toolID, cfg, err, now)
		return fmt.Errorf("Proton limite l'accès au compte (%v). Ne réessayez pas tout de suite : faites une demande sur https://proton.me/support/appeal-abuse, attendez la levée du blocage, puis reconnectez le compte.", err)
	case protonErrBadCreds:
		return errors.New("identifiants refusés par Proton (mot de passe ou code 2FA incorrect)")
	case protonErrAppVersion:
		return protonRecordFailure(toolID, cfg, err, now) // clear message, no pause
	case protonErrTransient:
		return fmt.Errorf("Proton injoignable, réessayez plus tard : %w", err)
	}
	return fmt.Errorf("connexion Proton refusée : %w", err)
}
