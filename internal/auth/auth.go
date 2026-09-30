package auth

import (
 "github.com/pquerna/otp/totp"
 "golang.org/x/crypto/bcrypt"
)
func HashPassword(p string)(string,error){ b,e:=bcrypt.GenerateFromPassword([]byte(p),bcrypt.DefaultCost); return string(b),e }
func CheckPassword(hash,p string)bool{return bcrypt.CompareHashAndPassword([]byte(hash),[]byte(p))==nil}
func VerifyTOTP(secret,code string)bool{return totp.Validate(code,secret)}
