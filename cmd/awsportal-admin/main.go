package main
import("context";"flag";"fmt";"log";"os";"github.com/pquerna/otp/totp";"github.com/sptree-m/awsportal/internal/auth";"github.com/sptree-m/awsportal/internal/store")
func main(){dbp:=flag.String("db","./awsportal.db","DB path");cmd:=flag.String("cmd","","create-user|add-instance|add-group|assign-group");user:=flag.String("user","","username");pass:=flag.String("password","","password");role:=flag.String("role","user","user|group_admin|portal_admin");iid:=flag.String("instance","","EC2 instance id");name:=flag.String("name","","name");host:=flag.String("host","","DCV host");group:=flag.String("group","","group name");flag.Parse();ctx:=context.Background();s,e:=store.Open(*dbp);if e!=nil{log.Fatal(e)};defer s.Close();if e=s.Migrate(ctx);e!=nil{log.Fatal(e)}
 switch *cmd{
 case "create-user":h,e:=auth.HashPassword(*pass);if e!=nil{log.Fatal(e)};secret:="";if *role=="portal_admin"{k,e:=totp.Generate(totp.GenerateOpts{Issuer:"awsportal",AccountName:*user});if e!=nil{log.Fatal(e)};secret=k.Secret();fmt.Println("TOTP URI:",k.URL())};if e=s.CreateUser(ctx,*user,h,*role,secret);e!=nil{log.Fatal(e)}
 case "add-instance":_,e=s.DB.ExecContext(ctx,"INSERT INTO instances(instance_id,name,dcv_host) VALUES(?,?,?)",*iid,*name,*host);if e!=nil{log.Fatal(e)}
 case "add-group":_,e=s.DB.ExecContext(ctx,"INSERT INTO groups(name) VALUES(?)",*group);if e!=nil{log.Fatal(e)}
 case "assign-group":_,e=s.DB.ExecContext(ctx,`INSERT INTO instance_groups(instance_id,group_id,can_control) SELECT i.id,g.id,1 FROM instances i,groups g WHERE i.instance_id=? AND g.name=?`,*iid,*group);if e!=nil{log.Fatal(e)}
 default:fmt.Fprintln(os.Stderr,"-cmd is required");os.Exit(2)}
}
