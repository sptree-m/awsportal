package main

import (
 "context"
 "embed"
 "html/template"
 "log"
 "net/http"
 "os"
 "time"

 "github.com/sptree-m/awsportal/internal/store"
)

//go:embed web/*
var web embed.FS

type app struct{ db *store.Store; tpl *template.Template }

func main() {
 db, err := store.Open(env("AWSPORTAL_DB", "./awsportal.db")); if err != nil { log.Fatal(err) }; defer db.Close()
 if err := db.Migrate(context.Background()); err != nil { log.Fatal(err) }
 tpl := template.Must(template.ParseFS(web, "web/*.html"))
 a := &app{db: db, tpl: tpl}
 mux := http.NewServeMux()
 mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request){ w.WriteHeader(http.StatusOK); _, _ = w.Write([]byte("ok")) })
 mux.HandleFunc("GET /", a.dashboard)
 s := &http.Server{Addr: env("AWSPORTAL_ADDR", ":8080"), Handler: securityHeaders(mux), ReadHeaderTimeout: 5*time.Second, IdleTimeout: 60*time.Second}
 log.Printf("awsportal listening on %s", s.Addr); log.Fatal(s.ListenAndServe())
}
func (a *app) dashboard(w http.ResponseWriter, r *http.Request){ _ = a.tpl.ExecuteTemplate(w, "index.html", map[string]any{"Title":"Infrastructure Portal"}) }
func securityHeaders(next http.Handler) http.Handler { return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){ w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'"); w.Header().Set("X-Content-Type-Options","nosniff"); w.Header().Set("Referrer-Policy","no-referrer"); next.ServeHTTP(w,r) }) }
func env(k,d string) string { if v:=os.Getenv(k); v!="" { return v }; return d }
