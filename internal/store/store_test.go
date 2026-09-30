package store
import("context";"path/filepath";"testing")
func TestRBACVisibility(t *testing.T){ctx:=context.Background();s,e:=Open(filepath.Join(t.TempDir(),"t.db"));if e!=nil{t.Fatal(e)};defer s.Close();if e=s.Migrate(ctx);e!=nil{t.Fatal(e)}
 _,e=s.DB.Exec(`INSERT INTO users(id,username,password_hash,role) VALUES(1,'u','x','user'),(2,'a','x','portal_admin'); INSERT INTO groups(id,name) VALUES(1,'g'); INSERT INTO group_members VALUES(1,1); INSERT INTO instances(id,instance_id,name,dcv_host) VALUES(1,'i-1','one','one.local'),(2,'i-2','two','two.local'); INSERT INTO instance_groups VALUES(1,1,1);`);if e!=nil{t.Fatal(e)}
 u:=User{ID:1,Username:"u",Role:"user",Enabled:true};xs,e:=s.VisibleInstances(ctx,u);if e!=nil{t.Fatal(e)};if len(xs)!=1||xs[0].InstanceID!="i-1"{t.Fatalf("unexpected visibility: %#v",xs)};if !s.CanControl(ctx,u,"i-1")||s.CanControl(ctx,u,"i-2"){t.Fatal("RBAC control error")}
 a:=User{ID:2,Username:"a",Role:"portal_admin",Enabled:true};xs,e=s.VisibleInstances(ctx,a);if e!=nil||len(xs)!=2{t.Fatalf("admin visibility error: %v %#v",e,xs)}
}
