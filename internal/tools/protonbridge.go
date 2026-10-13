package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Proton Bridge driver. Bridge has no scripting API besides its GUI gRPC, so the
// Hub drives the official admin shell (`bridge --cli`) through a pty: it stops
// the headless service (Bridge allows one instance), types the login the user
// gave in the Hub form, reads the Bridge-generated IMAP password with `info`,
// then restarts the service. The Proton password, the 2FA code and the mailbox
// password go straight from the HTTP body to the pty: they are never stored,
// logged or returned, and error text is scrubbed of them. Mail stays read-only
// for the Hub: only the IMAP side of Bridge is ever configured.

const (
	bridgeUnit        = "proton-bridge.service"
	bridgeDirEnv      = "HUB_PROTON_BRIDGE_DIR" // default: ./deploy/proton-bridge
	bridgeStepTimeout = 60 * time.Second
	bridgePendingTTL  = 3 * time.Minute
	bridgeAbuseWait   = 15 * time.Minute // client-side cool-down after an anti-abuse / human-verification refusal
	bridgeCaptchaTTL  = 10 * time.Minute // how long Bridge's CAPTCHA prompt waits for the user's Enter
)

// BridgeService controls the headless Bridge unit.
type BridgeService interface {
	Stop() error
	Start() error
	Active() bool
}

type systemdBridge struct{}

func systemctlUser(args ...string) *exec.Cmd {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Env = os.Environ()
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
	}
	return cmd
}

func (systemdBridge) Stop() error  { return runQuiet(systemctlUser("stop", bridgeUnit)) }
func (systemdBridge) Start() error { return runQuiet(systemctlUser("start", bridgeUnit)) }
func (systemdBridge) Active() bool {
	return systemctlUser("is-active", "--quiet", bridgeUnit).Run() == nil
}

func runQuiet(cmd *exec.Cmd) error {
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %v: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

var (
	bridgeService BridgeService = systemdBridge{}
	// bridgeCommand builds the admin shell command; tests replace it.
	bridgeCommand = defaultBridgeCommand
)

func bridgeDir() string {
	if d := os.Getenv(bridgeDirEnv); d != "" {
		return d
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, "deploy", "proton-bridge")
}

func defaultBridgeCommand() (*exec.Cmd, error) {
	dir := bridgeDir()
	if _, err := os.Stat(filepath.Join(dir, "env.sh")); err != nil {
		return nil, fmt.Errorf("Proton Bridge n'est pas installé (%s/env.sh introuvable) : lancez deploy/proton-bridge/install.sh", dir)
	}
	return exec.Command("bash", "-c", `. "$1/env.sh" && exec "$PB_BIN" --cli`, "bridge", dir), nil
}

// ---- pty session ----

var (
	ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	// OSC 8 hyperlink: ESC ] 8 ; params ; URL (BEL | ESC \). The URL is the real
	// target (the visible text may be cut), so it is kept as a separate token.
	osc8RE   = regexp.MustCompile(`\x1b\]8;[^;\x07\x1b]*;([^\x07\x1b]*)(?:\x07|\x1b\\)`)
	oscAnyRE = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
)

// cleanTerm turns the readline redraws of the shell into plain text lines.
func cleanTerm(b []byte) string {
	s := osc8RE.ReplaceAllString(string(b), " $1 ")
	s = oscAnyRE.ReplaceAllString(s, "")
	s = ansiRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\x08", "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

type bridgeShell struct {
	cmd   *exec.Cmd
	pty   *os.File
	mu    sync.Mutex
	buf   bytes.Buffer
	mark  int // start of the text not yet consumed
	done  chan struct{}
	wake  chan struct{}
	scrub []string // secrets to mask in any text leaving this type
	timer *time.Timer
	once  sync.Once
	// abort: kill the shell without typing anything more. Bridge waits on a human
	// verification for a keypress and any input (even our polite "exit") makes it
	// call Proton again with an unsolved CAPTCHA.
	abort bool
}

func startBridgeShell() (*bridgeShell, error) {
	cmd, err := bridgeCommand()
	if err != nil {
		return nil, err
	}
	// Very wide terminal: Bridge/readline never hard-wrap the verification link.
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 4000})
	if err != nil {
		return nil, fmt.Errorf("démarrage du shell Bridge : %w", err)
	}
	sh := &bridgeShell{cmd: cmd, pty: f, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	go func() {
		tmp := make([]byte, 4096)
		for {
			n, err := f.Read(tmp)
			if n > 0 {
				sh.mu.Lock()
				sh.buf.Write(tmp[:n])
				sh.mu.Unlock()
				select {
				case sh.wake <- struct{}{}:
				default:
				}
			}
			if err != nil {
				close(sh.done)
				return
			}
		}
	}()
	return sh, nil
}

// pending returns the cleaned, not yet consumed text.
func (sh *bridgeShell) pending() string {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return cleanTerm(sh.buf.Bytes()[sh.mark:])
}

func (sh *bridgeShell) markPos() int {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.mark
}

func (sh *bridgeShell) setMark(m int) {
	sh.mu.Lock()
	sh.mark = m
	sh.mu.Unlock()
}

func (sh *bridgeShell) consumeAll() {
	sh.mu.Lock()
	sh.mark = sh.buf.Len()
	sh.mu.Unlock()
}

// expect waits until the unconsumed text matches one of the patterns and
// consumes everything. It returns the pattern index and the text.
func (sh *bridgeShell) expect(ctx context.Context, timeout time.Duration, pats ...*regexp.Regexp) (int, string, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		text := sh.pending()
		for i, p := range pats {
			if p.MatchString(text) {
				sh.consumeAll()
				return i, text, nil
			}
		}
		select {
		case <-sh.wake:
		case <-sh.done:
			// Process gone: one last look at what it printed.
			text = sh.pending()
			for i, p := range pats {
				if p.MatchString(text) {
					sh.consumeAll()
					return i, text, nil
				}
			}
			return -1, sh.scrubbed(text), errors.New("le shell Bridge s'est arrêté")
		case <-deadline.C:
			return -1, sh.scrubbed(sh.pending()), errors.New("délai dépassé en attendant la réponse de Bridge")
		case <-ctx.Done():
			return -1, sh.scrubbed(sh.pending()), ctx.Err()
		}
	}
}

func (sh *bridgeShell) send(line string) error {
	_, err := sh.pty.WriteString(line + "\n")
	return err
}

func (sh *bridgeShell) scrubbed(s string) string {
	for _, secret := range sh.scrub {
		if len(secret) >= 4 {
			s = strings.ReplaceAll(s, secret, "***")
		}
	}
	return s
}

// close terminates the shell (politely first) and waits for it.
func (sh *bridgeShell) close() { sh.once.Do(sh.doClose) }

func (sh *bridgeShell) doClose() {
	if sh.timer != nil {
		sh.timer.Stop()
	}
	if sh.abort {
		_ = sh.cmd.Process.Kill()
	} else {
		_ = sh.send("exit")
	}
	select {
	case <-sh.done:
	case <-time.After(8 * time.Second):
		_ = sh.cmd.Process.Kill()
		select {
		case <-sh.done:
		case <-time.After(3 * time.Second):
		}
	}
	_ = sh.pty.Close()
	_ = sh.cmd.Wait()
	for i := range sh.scrub {
		sh.scrub[i] = ""
	}
}

var (
	reShellPrompt = regexp.MustCompile(`>>>\s*$`)
	reUsername    = regexp.MustCompile(`(?m)Username:\s*$`)
	rePassword    = regexp.MustCompile(`(?m)Password:\s*$`)
	reTwoFA       = regexp.MustCompile(`Two factor code:\s*$`)
	reMailboxPW   = regexp.MustCompile(`Mailbox password:\s*$`)
	reAdded       = regexp.MustCompile(`was added successfully`)
	reCannotLogin = regexp.MustCompile(`Cannot login: [^\n]*`)
	// Proton asks for a CAPTCHA before any 2FA step (code 9001, unknown client on
	// this IP). Bridge prints a https://verify.proton.me/?methods=..&token=.. link
	// and waits for Enter; the user solves it in their own browser and the Hub then
	// sends that Enter to the same shell. reCaptchaURL needs the terminating
	// whitespace so a half-printed link is never taken; reHuman (words only) is the
	// fallback when the link is not recognised.
	// Only verify.proton.me is the CAPTCHA: Bridge's error text also prints
	// https://mail-api.proton.me/auth/v4: and a support link, which are not it.
	reCaptchaURL = regexp.MustCompile(`https://verify\.proton\.me/\S+\s`)
	reTokenParam = regexp.MustCompile(`token=[^\s&]+`)
	reHuman      = regexp.MustCompile(`(?i)human verification|captcha`)
	reFill       = regexp.MustCompile(`Please fill [a-z ]+:\s*$`)
	reNoAccounts = regexp.MustCompile(`No active accounts`)
	reInfoIMAP   = regexp.MustCompile(`IMAP port:\s*(\d+)[\s\S]*?Username:\s*(\S+)[\s\S]*?Password:\s*(\S+)\s*\n`)
	reConfirm    = regexp.MustCompile(`(?i)are you sure[^\n]*`)
)

// ---- guided login (pending between HTTP calls) ----

// BridgeConnectInput is what the Hub form carries. Nothing is kept.
type BridgeConnectInput struct {
	Email           string
	Password        string
	TOTP            string
	MailboxPassword string
	CaptchaDone     bool // the user solved the CAPTCHA: send Bridge the Enter it waits for
}

// BridgeConnectResult tells the UI what to ask next. When Status is "added",
// IMAP* hold the credentials Bridge generated for the mail client.
type BridgeConnectResult struct {
	Status       string `json:"status"`        // needs_captcha | needs_2fa | needs_mailbox_password | added
	URL          string `json:"url,omitempty"` // needs_captcha: verification link to open in the user's browser
	Message      string `json:"message,omitempty"`
	Email        string `json:"-"`
	IMAPPort     int    `json:"-"`
	IMAPPassword string `json:"-"`
}

// BridgeError is a refusal shown as is, with the way forward. Kind is one of
// abuse | human | credentials | twofa | other.
type BridgeError struct {
	Kind string
	Msg  string
}

func (e *BridgeError) Error() string { return e.Msg }

type bridgePending struct {
	sh     *bridgeShell
	email  string
	step   string // needs_captcha | needs_2fa | needs_mailbox_password
	url    string // needs_captcha: the link Bridge printed
	humans int    // CAPTCHA prompts seen in this login: a second one ends it
}

var (
	bridgeMu      sync.Mutex // one Bridge operation at a time
	bridgePend    *bridgePending
	bridgeCoolOff time.Time
)

// bridgeHold keeps the login waiting for the next user input, for a bounded time.
func bridgeHold(p *bridgePending, step string) {
	p.step = step
	ttl := bridgePendingTTL
	if step == "needs_captcha" {
		ttl = bridgeCaptchaTTL
	}
	bridgePend = p
	if p.sh.timer != nil {
		p.sh.timer.Stop()
	}
	p.sh.timer = time.AfterFunc(ttl, func() {
		bridgeMu.Lock()
		defer bridgeMu.Unlock()
		if bridgePend == p {
			bridgePend = nil
			bridgeDrop(p)
		}
	})
}

// bridgeDrop ends the shell and brings the headless service back.
func bridgeDrop(p *bridgePending) {
	if p == nil {
		return
	}
	p.sh.close()
	_ = bridgeService.Start()
}

// BridgeConnect adds the Proton account to Bridge. A call with a password starts
// one login attempt (the previous unfinished one is dropped); later calls carry
// the 2FA code / mailbox password the previous result asked for. It never
// retries: any refusal ends the attempt and restarts the service.
func BridgeConnect(ctx context.Context, in BridgeConnectInput) (BridgeConnectResult, error) {
	bridgeMu.Lock()
	defer bridgeMu.Unlock()

	if in.Password != "" {
		bridgeDrop(bridgePend)
		bridgePend = nil
		return bridgeStartLogin(ctx, in)
	}
	p := bridgePend
	if p == nil {
		return BridgeConnectResult{}, &BridgeError{"other", "aucune connexion en cours (délai dépassé) : recommencez avec le mot de passe"}
	}
	return bridgeContinueLogin(ctx, p, in)
}

func bridgeStartLogin(ctx context.Context, in BridgeConnectInput) (res BridgeConnectResult, err error) {
	email := strings.TrimSpace(in.Email)
	if !strings.Contains(email, "@") {
		return res, &BridgeError{"other", "adresse Proton requise"}
	}
	if now := time.Now(); now.Before(bridgeCoolOff) {
		return res, &BridgeError{"abuse", fmt.Sprintf("Pause de sécurité jusqu'à %s après un refus anti-abus/vérification humaine de Proton : ne réessayez pas avant, pour ne pas aggraver le blocage.", bridgeCoolOff.In(protonLocation()).Format("15:04"))}
	}
	if err := bridgeService.Stop(); err != nil {
		return res, &BridgeError{"other", "impossible d'arrêter le service Bridge : " + err.Error()}
	}
	sh, err := startBridgeShell()
	if err != nil {
		_ = bridgeService.Start()
		return res, &BridgeError{"other", err.Error()}
	}
	sh.scrub = []string{in.Password, in.TOTP, in.MailboxPassword}
	p := &bridgePending{sh: sh, email: email}
	defer func() {
		if err != nil {
			bridgeDrop(p)
			bridgePend = nil
		}
	}()

	if _, txt, e := sh.expect(ctx, bridgeStepTimeout, reShellPrompt); e != nil {
		return res, &BridgeError{"other", "Bridge ne répond pas : " + e.Error() + lastLine(txt)}
	}

	// Already known account? Then no Proton login is needed at all.
	_ = sh.send("list")
	if _, txt, e := sh.expect(ctx, bridgeStepTimeout, reShellPrompt); e == nil && bridgeListHas(txt, email) {
		if bridgeListConnected(txt, email) {
			return bridgeFinish(ctx, p)
		}
	}

	_ = sh.send("login")
	idx, txt, e := sh.expect(ctx, bridgeStepTimeout, reUsername, rePassword)
	if e != nil {
		return res, &BridgeError{"other", "Bridge n'a pas demandé d'identifiant : " + e.Error() + lastLine(txt)}
	}
	if idx == 0 {
		_ = sh.send(email)
		if _, txt, e = sh.expect(ctx, bridgeStepTimeout, rePassword); e != nil {
			return res, &BridgeError{"other", "Bridge n'a pas demandé le mot de passe : " + e.Error() + lastLine(txt)}
		}
	}
	// The single password login of this click.
	_ = sh.send(in.Password)
	res, err = bridgeAfterSecret(ctx, p, in)
	return res, err
}

func bridgeContinueLogin(ctx context.Context, p *bridgePending, in BridgeConnectInput) (res BridgeConnectResult, err error) {
	p.sh.scrub = append(p.sh.scrub, in.TOTP, in.MailboxPassword)
	defer func() {
		if err != nil {
			bridgeDrop(p)
			bridgePend = nil
		}
	}()
	switch p.step {
	case "needs_captcha":
		if !in.CaptchaDone {
			return BridgeConnectResult{Status: "needs_captcha", URL: p.url, Message: bridgeMsgCaptcha}, nil
		}
		// The Enter Bridge waits for: it retries the Proton login with the solved CAPTCHA.
		_ = p.sh.send("")
	case "needs_2fa":
		code := strings.ReplaceAll(strings.TrimSpace(in.TOTP), " ", "")
		if code == "" {
			return BridgeConnectResult{Status: "needs_2fa", Message: bridgeMsg2FA}, nil
		}
		in.TOTP = code
		p.sh.scrub = append(p.sh.scrub, code)
		_ = p.sh.send(code)
	case "needs_mailbox_password":
		if in.MailboxPassword == "" {
			return BridgeConnectResult{Status: "needs_mailbox_password", Message: bridgeMsgMailbox}, nil
		}
		_ = p.sh.send(in.MailboxPassword)
	}
	return bridgeAfterSecret(ctx, p, in)
}

const (
	bridgeMsg2FA     = "Saisissez le code à 6 chiffres de votre application d'authentification (un code déjà utilisé pour l'agenda sera refusé : attendez le suivant)."
	bridgeMsgCaptcha = "Proton demande une vérification humaine : ouvrez le lien dans votre navigateur, résolvez le CAPTCHA, puis cliquez sur « J'ai résolu le CAPTCHA, continuer »."
	bridgeMsgMailbox = "Ce compte est en mode « deux mots de passe » : saisissez le mot de passe de la boîte."
)

// bridgeAfterSecret reads Bridge's reaction to a secret just typed.
func bridgeAfterSecret(ctx context.Context, p *bridgePending, in BridgeConnectInput) (BridgeConnectResult, error) {
	start := p.sh.markPos()
	idx, txt, err := p.sh.expect(ctx, bridgeStepTimeout, reTwoFA, reMailboxPW, reAdded, reCannotLogin, reFill, reCaptchaURL, reHuman)
	switch {
	case err != nil:
		return BridgeConnectResult{}, bridgeLoginError(p.sh.scrubbed(txt), err)
	case idx == 0:
		bridgeHold(p, "needs_2fa")
		return BridgeConnectResult{Status: "needs_2fa", Message: bridgeMsg2FA}, nil
	case idx == 1:
		bridgeHold(p, "needs_mailbox_password")
		return BridgeConnectResult{Status: "needs_mailbox_password", Message: bridgeMsgMailbox}, nil
	case idx == 2:
		// Typed-ahead input waits in the pty until the shell is back at its prompt.
		bridgePend = nil
		return bridgeFinish(ctx, p)
	case idx == 5 || idx == 6:
		return bridgeCaptcha(ctx, p, start, txt, idx == 5)
	}
	return BridgeConnectResult{}, bridgeLoginError(p.sh.scrubbed(txt), nil)
}

// bridgeCaptcha holds the login on Bridge's CAPTCHA prompt and returns its link.
// Nothing is typed meanwhile (any input makes Bridge call Proton again). Without
// a readable link, or on a second CAPTCHA in the same login, the shell is killed
// untouched and the cool-off starts.
func bridgeCaptcha(ctx context.Context, p *bridgePending, start int, txt string, haveURL bool) (BridgeConnectResult, error) {
	fail := func() (BridgeConnectResult, error) {
		p.sh.abort = true
		bridgeCoolOff = time.Now().Add(bridgeAbuseWait)
		return BridgeConnectResult{}, bridgeHumanError()
	}
	p.humans++
	if p.humans > 1 {
		return fail()
	}
	if !haveURL {
		// Words came first: give the link a moment to finish printing.
		p.sh.setMark(start)
		if _, t2, e := p.sh.expect(ctx, 3*time.Second, reCaptchaURL); e == nil {
			txt = t2
		} else {
			return fail()
		}
	}
	link := captchaLink(txt)
	if link == "" {
		// Diagnostic only: Bridge's own text, secrets scrubbed, tokens masked.
		log.Printf("proton-bridge: no verify.proton.me link in CAPTCHA output: %q",
			reTokenParam.ReplaceAllString(p.sh.scrubbed(txt), "token=***"))
		return fail()
	}
	p.url = link
	bridgeHold(p, "needs_captcha")
	return BridgeConnectResult{Status: "needs_captcha", URL: link, Message: bridgeMsgCaptcha}, nil
}

// captchaLink extracts the verification link from Bridge's output. Only an
// https URL whose parsed host is exactly verify.proton.me is accepted (error
// URLs such as mail-api.proton.me/auth/v4: and support links are not the
// CAPTCHA). A link cut over several lines is rejoined: the following lines are
// glued as long as they are a single whitespace-free token and the cut line
// looked like a full row. Returns "" when there is no such link.
func captchaLink(txt string) string {
	lines := strings.Split(txt, "\n")
	for i, l := range lines {
		for _, loc := range reCaptchaURL.FindAllStringIndex(l+" ", -1) {
			link := strings.TrimSpace((l + " ")[loc[0]:loc[1]])
			if loc[1] >= len(l) && len(strings.TrimSpace(l)) >= 60 {
				for j := i + 1; j < len(lines); j++ {
					next := strings.TrimSpace(lines[j])
					if next == "" || strings.ContainsAny(next, " \t") || strings.HasPrefix(next, ">>>") {
						break
					}
					link += next
					if len(next) < 60 {
						break
					}
				}
			}
			link = strings.TrimRight(link, ".,;:)>\"'")
			if verifyLinkOK(link) {
				return link
			}
		}
	}
	return ""
}

// verifyLinkOK reports whether s is an https URL on exactly verify.proton.me.
func verifyLinkOK(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host == "verify.proton.me" && u.User == nil
}

// BridgeCancel ends a login waiting for the user (CAPTCHA, code, mailbox
// password) without typing anything into Bridge: the shell is killed, which makes
// Bridge send nothing more to Proton, and the headless service comes back. It
// reports whether there was something to cancel.
func BridgeCancel() bool {
	bridgeMu.Lock()
	defer bridgeMu.Unlock()
	p := bridgePend
	if p == nil {
		return false
	}
	bridgePend = nil
	p.sh.abort = true
	bridgeDrop(p)
	return true
}

// bridgeFinish reads the IMAP credentials of the account and hands the shell
// back (service restart included).
func bridgeFinish(ctx context.Context, p *bridgePending) (BridgeConnectResult, error) {
	defer func() { bridgeDrop(p); bridgePend = nil }()
	p.sh.consumeAll()
	_ = p.sh.send("info " + p.email)
	_, txt, err := p.sh.expect(ctx, bridgeStepTimeout, reInfoIMAP)
	if err != nil {
		return BridgeConnectResult{}, &BridgeError{"other", "Compte ajouté mais « info » n'a pas répondu : " + err.Error() + lastLine(p.sh.scrubbed(txt))}
	}
	m := reInfoIMAP.FindStringSubmatch(txt)
	port, _ := strconv.Atoi(m[1])
	if port == 0 || m[3] == "" {
		return BridgeConnectResult{}, &BridgeError{"other", "Compte ajouté mais les paramètres IMAP de Bridge sont illisibles"}
	}
	return BridgeConnectResult{Status: "added", Email: p.email, IMAPPort: port, IMAPPassword: m[3]}, nil
}

func lastLine(txt string) string {
	lines := strings.Split(strings.TrimSpace(txt), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, ">>>") {
			return " (" + l + ")"
		}
	}
	return ""
}

func bridgeListHas(txt, email string) bool {
	return strings.Contains(strings.ToLower(txt), strings.ToLower(email))
}

// bridgeListConnected reads the account state of the `list` line, e.g.
// "0 : user@proton.me (connected, split)". Unknown format: not connected, so
// the regular login runs (and Bridge itself reports a duplicate).
func bridgeListConnected(txt, email string) bool {
	for _, l := range strings.Split(txt, "\n") {
		low := strings.ToLower(l)
		if strings.Contains(low, strings.ToLower(email)) {
			return strings.Contains(low, "connected") && !strings.Contains(low, "disconnected") && !strings.Contains(low, "logged out")
		}
	}
	return false
}

// bridgeLoginError renders a Bridge refusal with the way forward. The line is
// Bridge's own, shown as is (scrubbed of secrets by the caller).
func bridgeLoginError(txt string, cause error) error {
	line := strings.TrimSpace(reCannotLogin.FindString(txt))
	if line == "" {
		line = strings.TrimSpace(lastLine(txt))
		line = strings.Trim(line, "()")
	}
	if line == "" && cause != nil {
		line = cause.Error()
	}
	low := strings.ToLower(line + " " + txt)
	switch {
	case strings.Contains(low, "2028") || strings.Contains(low, "abuse") || strings.Contains(low, "too many") || strings.Contains(low, "429") || strings.Contains(low, "rate"):
		bridgeCoolOff = time.Now().Add(bridgeAbuseWait)
		return &BridgeError{"abuse", fmt.Sprintf("Proton limite l'accès au compte : %s. Ne réessayez pas tout de suite (pause de 15 min dans le Hub) : faites une demande sur https://proton.me/support/appeal-abuse, attendez la levée du blocage puis recommencez. Aucun nouvel essai n'est fait automatiquement.", line)}
	case strings.Contains(low, "captcha") || strings.Contains(low, "human") || strings.Contains(low, "9001"):
		bridgeCoolOff = time.Now().Add(bridgeAbuseWait)
		return bridgeHumanError()
	case strings.Contains(low, "2fa") || strings.Contains(low, "two factor") || strings.Contains(low, "two-factor") || strings.Contains(low, "authentication code"):
		return &BridgeError{"twofa", fmt.Sprintf("Code 2FA refusé par Proton (%s). La tentative est terminée : recommencez avec « Connecter » et le prochain code de votre application.", line)}
	case strings.Contains(low, "password") || strings.Contains(low, "credential") || strings.Contains(low, "8002"):
		return &BridgeError{"credentials", fmt.Sprintf("Identifiants refusés par Proton (%s). Vérifiez l'adresse et le mot de passe, puis recommencez (une seule tentative par clic).", line)}
	}
	return &BridgeError{"other", fmt.Sprintf("Connexion à Proton refusée par Bridge : %s", line)}
}

func bridgeHumanError() error {
	return &BridgeError{"human", "La vérification humaine (CAPTCHA) de Proton n'a pas abouti (refusée, répétée ou lien illisible). La tentative est arrêtée sans réessai automatique (pause de 15 min dans le Hub). Recommencez une fois la pause terminée : le Hub affichera le lien de vérification à ouvrir dans votre navigateur ; si le blocage persiste : https://proton.me/support/appeal-abuse."}
}

// ---- disconnect ----

// BridgeDisconnect removes the account from Bridge (`delete`), which also drops
// its stored session; the Hub forgets the IMAP password separately.
func BridgeDisconnect(ctx context.Context, email string) error {
	bridgeMu.Lock()
	defer bridgeMu.Unlock()
	bridgeDrop(bridgePend)
	bridgePend = nil
	if err := bridgeService.Stop(); err != nil {
		return fmt.Errorf("impossible d'arrêter le service Bridge : %w", err)
	}
	sh, err := startBridgeShell()
	if err != nil {
		_ = bridgeService.Start()
		return err
	}
	p := &bridgePending{sh: sh, email: email}
	defer bridgeDrop(p)
	if _, txt, e := sh.expect(ctx, bridgeStepTimeout, reShellPrompt); e != nil {
		return fmt.Errorf("Bridge ne répond pas : %w%s", e, lastLine(txt))
	}
	_ = sh.send("list")
	_, txt, _ := sh.expect(ctx, bridgeStepTimeout, reShellPrompt)
	if reNoAccounts.MatchString(txt) || !bridgeListHas(txt, email) {
		return nil // nothing to remove
	}
	_ = sh.send("delete " + email)
	idx, txt, e := sh.expect(ctx, bridgeStepTimeout, reConfirm, reShellPrompt)
	if e != nil {
		return fmt.Errorf("suppression du compte dans Bridge : %w%s", e, lastLine(txt))
	}
	if idx == 0 {
		_ = sh.send("yes")
		if _, txt, e = sh.expect(ctx, bridgeStepTimeout, reShellPrompt); e != nil {
			return fmt.Errorf("suppression du compte dans Bridge : %w%s", e, lastLine(txt))
		}
	}
	_ = sh.send("list")
	if _, txt, e = sh.expect(ctx, bridgeStepTimeout, reShellPrompt); e == nil && bridgeListHas(txt, email) {
		return errors.New("Bridge a conservé le compte après « delete » ; réessayez ou utilisez proton-bridge-cli")
	}
	return nil
}

// ---- probes ----

// BridgeProbe is the host-level state of Bridge, without touching accounts.
type BridgeProbe struct {
	Installed     bool   `json:"installed"`
	ServiceActive bool   `json:"serviceActive"`
	Busy          bool   `json:"busy"`              // a connect/disconnect is in progress
	Pending       string `json:"pending,omitempty"` // waiting for needs_captcha | needs_2fa | needs_mailbox_password
	CaptchaURL    string `json:"captchaUrl,omitempty"`
}

func BridgeProbeNow() BridgeProbe {
	pr := BridgeProbe{ServiceActive: bridgeService.Active()}
	if _, err := bridgeCommand(); err == nil {
		pr.Installed = true
	}
	if bridgeMu.TryLock() {
		if bridgePend != nil {
			pr.Pending = bridgePend.step
			pr.CaptchaURL = bridgePend.url
		}
		bridgeMu.Unlock()
	} else {
		pr.Busy = true
	}
	return pr
}

// BridgeIMAPReady reports whether the Bridge IMAP answers with the given
// credentials (login + mailbox list). Used to wait for the first sync.
func BridgeIMAPReady(cfg map[string]string) error {
	c, err := imapDial(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Login(cfg["username"], cfg["password"]).Wait(); err != nil {
		return err
	}
	boxes, err := c.List("", "*", nil).Collect()
	if err != nil {
		return err
	}
	if len(boxes) == 0 {
		return errors.New("aucun dossier visible pour l'instant (synchronisation en cours)")
	}
	return nil
}
