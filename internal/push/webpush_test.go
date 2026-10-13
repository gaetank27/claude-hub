package push

import (
	"crypto/ecdh"
	"encoding/base64"
	"strings"
	"testing"
)

// The worked example of RFC 8291, Appendix A.
func TestEncryptRFC8291Vector(t *testing.T) {
	b64 := base64.RawURLEncoding
	asPrivate, _ := b64.DecodeString("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	salt, _ := b64.DecodeString("DGv6ra1nlYgDCS1FRnbzlw")
	sender, err := ecdh.P256().NewPrivateKey(asPrivate)
	if err != nil {
		t.Fatal(err)
	}
	got, err := encrypt(
		[]byte("When I grow up, I want to be a watermelon"),
		"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		"BTBZMqHH6r4Tts7J_aSIgg",
		sender, salt,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Fatalf("ciphertext differs from the RFC 8291 example:\n got %s\nwant %s", b64.EncodeToString(got), want)
	}
}

func TestEncryptRejectsOversizedPayload(t *testing.T) {
	_, err := Encrypt([]byte(strings.Repeat("x", maxBody)),
		"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4", "BTBZMqHH6r4Tts7J_aSIgg")
	if err == nil {
		t.Fatal("expected an error for a payload over the WebPush limit")
	}
}
