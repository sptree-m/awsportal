package main

import (
 "net/http"
 "net/http/httptest"
 "os"
 "strings"
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


func TestStaticAssetsServed(t *testing.T) {
 h := staticHandler(http.StripPrefix("/static/", http.FileServer(http.FS(web))))
 cases := []struct{
  path string
  contentType string
  prefix string
 }{
  {"/static/web/app.css", "text/css", "@font-face"},
  {"/static/web/dashboard.js", "text/javascript", "(()=>"},
  {"/static/web/fonts/rounded-mplus-1mn-regular.ttf", "font/ttf", ""},
  {"/static/web/fonts/rounded-mplus-1mn-bold.ttf", "font/ttf", ""},
 }
 for _, tc := range cases {
  t.Run(tc.path, func(t *testing.T) {
   r := httptest.NewRequest(http.MethodGet, tc.path, nil)
   w := httptest.NewRecorder()
   h.ServeHTTP(w, r)
   if w.Code != http.StatusOK {
    t.Fatalf("%s returned %d", tc.path, w.Code)
   }
   if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.contentType) {
    t.Fatalf("%s content-type=%q want prefix %q", tc.path, got, tc.contentType)
   }
   if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "max-age=86400") {
    t.Fatalf("%s cache-control=%q", tc.path, got)
   }
   if tc.prefix != "" && !strings.HasPrefix(w.Body.String(), tc.prefix) {
    t.Fatalf("%s unexpected body prefix", tc.path)
   }
   if strings.HasSuffix(tc.path, ".ttf") && w.Body.Len() < 10000 {
    t.Fatalf("%s font response too small: %d", tc.path, w.Body.Len())
   }
  })
 }
}
