package auth

import (
	"bytes"
	"encoding/base64"
	"image/png"

	"github.com/pquerna/otp/totp"
)

// enrolment is what the wizard needs to display a TOTP enrolment: the otpauth://
// URI, the raw base32 secret (for manual entry) and a ready-to-render QR PNG.
type enrolment struct {
	secret  string // base32, stored encrypted; also surfaced for manual entry
	uri     string // otpauth:// provisioning URI
	qrPNG   string // base64-encoded PNG of the URI
	encrypted string // secretbox ciphertext of the secret, persisted in users.totp_secret_enc
}

// newEnrolment generates a fresh TOTP secret for account, renders its QR code and
// encrypts the secret at rest with the configured box.
func (a *Authenticator) newEnrolment(account string) (enrolment, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      a.cfg.TOTPIssuer,
		AccountName: account,
	})
	if err != nil {
		return enrolment{}, err
	}
	img, err := key.Image(256, 256)
	if err != nil {
		return enrolment{}, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return enrolment{}, err
	}
	enc, err := a.cfg.Secrets.Encrypt([]byte(key.Secret()))
	if err != nil {
		return enrolment{}, err
	}
	return enrolment{
		secret:    key.Secret(),
		uri:       key.URL(),
		qrPNG:     base64.StdEncoding.EncodeToString(buf.Bytes()),
		encrypted: enc,
	}, nil
}

// validateCode decrypts the user's stored TOTP secret and checks a submitted
// code against it (with the library's default ±1 step skew tolerance).
func (a *Authenticator) validateCode(secretEnc, code string) (bool, error) {
	raw, err := a.cfg.Secrets.Decrypt(secretEnc)
	if err != nil {
		return false, err
	}
	return totp.Validate(code, string(raw)), nil
}
