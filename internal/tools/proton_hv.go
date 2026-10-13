package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	proton "github.com/ProtonMail/go-proton-api"
)

// Proton human verification (Code=9001) for the calendar login. The API answers
// the auth request with Details = {HumanVerificationMethods, HumanVerificationToken}.
// The user solves the CAPTCHA on https://verify.proton.me/?methods=<m>&token=<t>
// (format of Bridge's own link) in their own browser, which marks the token as
// verified server side; the auth is then sent again, once, carrying that token
// in x-pm-human-verification-token / -token-type: captcha.
//
// While waiting, the password is held in memory only (zeroed on confirmation,
// cancellation and expiry), never logged nor stored.

const protonHVHost = "verify.proton.me"

var protonHVTTL = 10 * time.Minute // var: shortened by tests

type protonHV struct {
	username string
	password []byte
	token    string
	methods  []string
	link     string
	expires  time.Time
	timer    *time.Timer
}

var (
	protonHVs   = map[string]*protonHV{}
	protonHVsMu sync.Mutex
)

var protonHVMethodRE = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// discard wipes the held secrets; nil-safe.
func (h *protonHV) discard() {
	if h == nil {
		return
	}
	if h.timer != nil {
		h.timer.Stop()
	}
	for i := range h.password {
		h.password[i] = 0
	}
	h.token = ""
}

func (h *protonHV) result() ProtonConnectResult {
	exp := h.expires
	return ProtonConnectResult{
		Status:    "needs_captcha",
		Message:   "Proton demande une vérification humaine (CAPTCHA). Ouvrez le lien dans votre navigateur, résolvez-la, puis cliquez sur « J'ai résolu le CAPTCHA, continuer ».",
		URL:       h.link,
		ExpiresAt: &exp,
	}
}

func protonHVPeek(toolID string) *protonHV {
	protonHVsMu.Lock()
	defer protonHVsMu.Unlock()
	return protonHVs[toolID]
}

// protonHVTake removes and returns the open wait (the caller owns its secrets).
func protonHVTake(toolID string) *protonHV {
	protonHVsMu.Lock()
	h := protonHVs[toolID]
	delete(protonHVs, toolID)
	protonHVsMu.Unlock()
	if h != nil && h.timer != nil {
		h.timer.Stop()
	}
	return h
}

func protonHVDrop(toolID string) {
	protonHVTake(toolID).discard()
}

// ProtonConnectCancel ends a CAPTCHA wait or a half-done login without sending
// anything to Proton, wiping the password held in memory. It reports whether
// something was pending.
func ProtonConnectCancel(toolID string) bool {
	had := protonHVPeek(toolID) != nil
	protonHVDrop(toolID)
	protonPendingsMu.Lock()
	_, hadPending := protonPendings[toolID]
	protonPendingsMu.Unlock()
	protonDropPending(toolID, true)
	return had || hadPending
}

// protonVerifyLink builds the verification link and enforces https + exact host
// after parsing. Methods and token are validated / escaped.
func protonVerifyLink(methods []string, token string) (string, error) {
	var ms []string
	for _, m := range methods {
		m = strings.ToLower(strings.TrimSpace(m))
		if !protonHVMethodRE.MatchString(m) {
			return "", fmt.Errorf("méthode de vérification inattendue %q", m)
		}
		ms = append(ms, m)
	}
	if len(ms) == 0 {
		return "", errors.New("aucune méthode de vérification fournie")
	}
	if token == "" || len(token) > 1024 || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("jeton de vérification absent ou de forme inattendue")
	}
	link := "https://" + protonHVHost + "/?methods=" + strings.Join(ms, ",") + "&token=" + url.QueryEscape(token)
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.Host != protonHVHost || u.User != nil {
		return "", errors.New("lien de vérification invalide")
	}
	return link, nil
}

// protonStartCaptcha handles the first 9001: it keeps what the retry needs and
// returns the link. No pause (an expected step, not a refusal) and no request.
// When the answer has no usable token, the scrubbed structured response is
// logged for diagnosis and the user is told; still no pause.
func protonStartCaptcha(toolID, username, password string, err error) (ProtonConnectResult, error) {
	var apiErr *proton.APIError
	if !errors.As(err, &apiErr) {
		return ProtonConnectResult{}, err
	}
	hv, derr := apiErr.GetHVDetails()
	var link string
	if derr == nil {
		var methods []string
		hasCaptcha := false
		for _, m := range hv.Methods {
			if strings.EqualFold(m, "captcha") {
				hasCaptcha = true
			}
			methods = append(methods, m)
		}
		if hasCaptcha {
			link, derr = protonVerifyLink(methods, hv.Token)
		} else {
			derr = fmt.Errorf("Proton ne propose pas le CAPTCHA (méthodes : %s)", strings.Join(hv.Methods, ", "))
		}
	}
	if derr != nil {
		log.Printf("proton: human verification without usable token (%v): %s", derr, protonScrubAPIError(apiErr))
		return ProtonConnectResult{}, fmt.Errorf("Proton demande une vérification humaine mais n'a pas fourni de lien exploitable (%v). Détails techniques dans le journal du Hub (secrets masqués). Aucune pause appliquée, aucune nouvelle tentative automatique.", derr)
	}
	h := &protonHV{
		username: username,
		password: []byte(password),
		token:    hv.Token,
		methods:  hv.Methods,
		link:     link,
		expires:  time.Now().Add(protonHVTTL),
	}
	h.timer = time.AfterFunc(protonHVTTL, func() {
		protonHVsMu.Lock()
		if protonHVs[toolID] == h {
			delete(protonHVs, toolID)
		} else {
			h = nil
		}
		protonHVsMu.Unlock()
		h.discard()
	})
	protonHVsMu.Lock()
	old := protonHVs[toolID]
	protonHVs[toolID] = h
	protonHVsMu.Unlock()
	old.discard()
	return h.result(), nil
}

var protonTokenFieldRE = regexp.MustCompile(`(?i)("[A-Za-z]*(?:token|password|secret|proof|ephemeral)[A-Za-z]*"\s*:\s*)"[^"]*"`)

// protonScrubAPIError renders an API error for the log with tokens masked.
func protonScrubAPIError(e *proton.APIError) string {
	details := string(e.Details)
	if json.Valid(e.Details) {
		details = protonTokenFieldRE.ReplaceAllString(details, `$1"***"`)
	} else {
		details = "(non-JSON, masqué)"
	}
	if len(details) > 600 {
		details = details[:600] + "…"
	}
	return fmt.Sprintf("status=%d code=%d error=%q details=%s", e.Status, e.Code, e.Message, details)
}
