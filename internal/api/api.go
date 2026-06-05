package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rexxzar/autodevops/internal/auth"
	"github.com/Rexxzar/autodevops/internal/sse"
)

type Server struct {
	db   *pgxpool.Pool
	auth *auth.Auth
	hub  *sse.Hub
}

func NewServer(db *pgxpool.Pool, a *auth.Auth, hub *sse.Hub) http.Handler {
	s := &Server{db: db, auth: a, hub: hub}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(requestLogger)
	r.Use(corsMiddleware)

	r.Get("/healthz", s.handleHealth)
	r.Post("/auth/login", s.handleLogin)

	r.Group(func(r chi.Router) {
		r.Use(a.Middleware)
		r.Get("/events", hub.Handler())
		r.Get("/deployments", s.handleListDeployments)
		r.Get("/clusters", s.handleListClusters)
		r.Get("/cloud/accounts", s.handleListCloudAccounts)
	})

	return r
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration", time.Since(start),
		)
	})
}
