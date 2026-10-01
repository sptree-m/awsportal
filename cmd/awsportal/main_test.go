package main

import (
 "os"
 "testing"
 "github.com/sptree-m/awsportal/internal/store"
)

func TestLabDebugMFABypass(t *testing.T) {
 old, had := os.LookupEnv("AWSPORTAL_LAB_DEBUG_AUTH")
 defer func(){ if had { _ = os.Setenv("AWSPORTAL_LAB_DEBUG_AUTH", old) } else { _ = os.Unsetenv("AWSPORTAL_LAB_DEBUG_AUTH") } }()
 cases := []struct{name, flag, user string; want bool}{
  {"lab flag plus labdebug", "1", "labdebug", true},
  {"flag off", "0", "labdebug", false},
  {"other admin never bypasses", "1", "labadmin", false},
 }
 for _,tc := range cases {
  t.Run(tc.name, func(t *testing.T){
   _ = os.Setenv("AWSPORTAL_LAB_DEBUG_AUTH", tc.flag)
   got := labDebugMFABypass(store.User{Username:tc.user, Role:"portal_admin"})
   if got != tc.want { t.Fatalf("got %v want %v", got, tc.want) }
  })
 }
}
