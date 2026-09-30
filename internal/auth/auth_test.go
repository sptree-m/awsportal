package auth
import "testing"
func TestPassword(t *testing.T){h,e:=HashPassword("Correct-Horse-42");if e!=nil{t.Fatal(e)};if !CheckPassword(h,"Correct-Horse-42"){t.Fatal("正しいパスワードを拒否")};if CheckPassword(h,"wrong"){t.Fatal("誤ったパスワードを許可")}}
