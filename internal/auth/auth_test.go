package auth
import("strings";"testing";"time")
func TestPassword(t *testing.T){h,e:=HashPassword("Correct-Horse-42");if e!=nil{t.Fatal(e)};if !CheckPassword(h,"Correct-Horse-42"){t.Fatal("正しいパスワードを拒否")};if CheckPassword(h,"wrong"){t.Fatal("誤ったパスワードを許可")}}
func TestRFC6238SHA1Vectors(t *testing.T){secret:="GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ";cases:=[]struct{sec int64;want string}{{59,"94287082"},{1111111109,"07081804"},{1111111111,"14050471"},{1234567890,"89005924"},{2000000000,"69279037"},{20000000000,"65353130"}};for _,tc:=range cases{got,e:=TOTPAt(secret,time.Unix(tc.sec,0),8);if e!=nil||got!=tc.want{t.Fatalf("%d got=%s want=%s err=%v",tc.sec,got,tc.want,e)}}}
func TestGenerateSecretAndURI(t *testing.T){s,e:=GenerateTOTPSecret();if e!=nil{t.Fatal(e)};if len(s)<16{t.Fatal("secret too short")};u:=TOTPURI(s,"awsportal","admin01");if !strings.HasPrefix(u,"otpauth://totp/")||!strings.Contains(u,"secret="+s)||!strings.Contains(u,"issuer=awsportal"){t.Fatalf("bad uri: %s",u)}}
