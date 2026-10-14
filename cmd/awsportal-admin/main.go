package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/sptree-m/awsportal/internal/auth"
	"github.com/sptree-m/awsportal/internal/store"
	"log"
	"os"
	"strings"
	"time"
)

func main() {
	dbp := flag.String("db", "./awsportal.db", "DB path")
	cmd := flag.String("cmd", "", "create-user|add-instance|add-group|assign-group|configure-dcv|break-glass-admin|invite-passkey|reset-passkeys")
	user := flag.String("user", "", "username")
	pass := flag.String("password", "", "password")
	role := flag.String("role", "user", "user|group_admin|portal_admin")
	iid := flag.String("instance", "", "EC2 instance id")
	name := flag.String("name", "", "name")
	host := flag.String("host", "", "DCV host")
	group := flag.String("group", "", "group name")
	dcvMode := flag.String("dcv-mode", "native", "native only")
	tokenFile := flag.String("dcv-token-file", "", "Root-only machine credential file")
	reason := flag.String("reason", "", "identity verification / maintenance reason (required for passkeys)")
	flag.Parse()
	ctx := context.Background()
	s, e := store.Open(*dbp)
	if e != nil {
		log.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		log.Fatal(e)
	}
	switch *cmd {
	case "create-user":
		if *role != "portal_admin" {
			code, err := auth.NewPasskeyToken()
			if err == nil {
				err = s.CreatePasskeyUserCLI(ctx, *user, *role, code, time.Now())
			}
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println("パスキー登録コード（30分・登録専用）:", code)
			break
		}
		if len(*pass) < 12 {
			log.Fatal("admin password must contain at least 12 characters")
		}
		h, err := auth.HashPassword(*pass)
		if err != nil {
			log.Fatal(err)
		}
		secret, err := auth.GenerateTOTPSecret()
		if err != nil {
			log.Fatal(err)
		}
		if err = s.CreateUser(ctx, *user, h, *role, secret); err != nil {
			log.Fatal(err)
		}
		fmt.Println("TOTP URI:", auth.TOTPURI(secret, "awsportal", *user))
	case "invite-passkey", "reset-passkeys":
		code, err := auth.NewPasskeyToken()
		if err == nil {
			err = s.IssuePasskeyInviteCLI(ctx, *user, *reason, code, *cmd == "reset-passkeys", time.Now())
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("パスキー登録コード（30分・登録専用）:", code)
	case "break-glass-admin":
		if *user == "" || len(*pass) < 12 {
			log.Fatal("-user and -password are required")
		}
		h, e := auth.HashPassword(*pass)
		if e != nil {
			log.Fatal(e)
		}
		secret, e := auth.GenerateTOTPSecret()
		if e != nil {
			log.Fatal(e)
		}
		if e = s.BreakGlassResetAdmin(ctx, *user, h, secret); e != nil {
			log.Fatal(e)
		}
		fmt.Println("TOTP URI:", auth.TOTPURI(secret, "awsportal", *user))
		fmt.Println("管理者復旧完了。次回ログイン後にパスワード変更が必要です。")
	case "add-instance":
		_, e = s.DB.ExecContext(ctx, "INSERT INTO instances(instance_id,name,dcv_host) VALUES(?,?,?)", *iid, *name, *host)
		if e != nil {
			log.Fatal(e)
		}
	case "configure-dcv":
		b, e := os.ReadFile(*tokenFile)
		if e != nil {
			log.Fatal(e)
		}
		if e = s.ConfigureDCV(ctx, store.User{Role: "portal_admin"}, *iid, *host, *dcvMode, strings.TrimSpace(string(b))); e != nil {
			log.Fatal(e)
		}
	case "add-group":
		_, e = s.DB.ExecContext(ctx, "INSERT INTO groups(name) VALUES(?)", *group)
		if e != nil {
			log.Fatal(e)
		}
	case "assign-group":
		_, e = s.DB.ExecContext(ctx, `INSERT INTO instance_groups(instance_id,group_id,can_control) SELECT i.id,g.id,1 FROM instances i,groups g WHERE i.instance_id=? AND g.name=?`, *iid, *group)
		if e != nil {
			log.Fatal(e)
		}
	default:
		fmt.Fprintln(os.Stderr, "-cmd is required")
		os.Exit(2)
	}
}
