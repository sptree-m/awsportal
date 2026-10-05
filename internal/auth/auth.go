package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func HashPassword(p string) (string, error) {
	b, e := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(b), e
}
func CheckPassword(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

func GenerateTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

func TOTPURI(secret, issuer, account string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func VerifyTOTP(secret, code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	now := time.Now().Unix() / 30
	for d := int64(-1); d <= 1; d++ {
		v, e := totpAtCounter(secret, uint64(now+d), 6)
		if e == nil && hmac.Equal([]byte(v), []byte(code)) {
			return true
		}
	}
	return false
}

func TOTPAt(secret string, t time.Time, digits int) (string, error) {
	return totpAtCounter(secret, uint64(t.Unix()/30), digits)
}

func totpAtCounter(secret string, counter uint64, digits int) (string, error) {
	clean := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(clean)
	if e != nil {
		return "", e
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0"+strconv.Itoa(digits)+"d", bin%mod), nil
}
