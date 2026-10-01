// Package api is the HTTP interface of the ledger.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"razao/internal/ledger"
	"razao/internal/loadgen"
	"razao/internal/pipeline"
	"razao/internal/store"
	"razao/web"
)

// Options configure the server.
type Options struct {
	Demo   bool   // enables /v1/demo: seed, load test, tampering, reset
	APIKey string // when set, writes need "Authorization: Bearer <key>"
}

// Server holds the HTTP handlers and their shared state.
type Server struct {
	st  *store.Store
	p   *pipeline.Pipeline
	opt Options

	// recent is a small ring of the latest transactions for the live feed.
	mu     sync.Mutex
	recent []*ledger.Transaction

	loadMu     sync.Mutex
	loadCancel context.CancelFunc
	loadReport atomic.Pointer[loadgen.Report]
	tampered   *store.Tampering
}

const recentSize = 64

// New wires a server to a store and its writer.
func New(st *store.Store, p *pipeline.Pipeline, opt Options) *Server {
	s := &Server{st: st, p: p, opt: opt}
	p.OnCommit = func(txs []*ledger.Transaction) {
		if len(txs) > recentSize {
			txs = txs[len(txs)-recentSize:]
		}
		s.mu.Lock()
		s.recent = append(s.recent, txs...)
		if n := len(s.recent); n > recentSize {
			s.recent = append([]*ledger.Transaction(nil), s.recent[n-recentSize:]...)
		}
		s.mu.Unlock()
	}
	return s
}

// Handler builds the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/info", s.info)

	mux.HandleFunc("GET /v1/accounts", s.listAccounts)
	mux.HandleFunc("POST /v1/accounts", s.createAccount)
	mux.HandleFunc("GET /v1/accounts/{ref}", s.getAccount)
	mux.HandleFunc("GET /v1/accounts/{ref}/statement", s.statement)

	mux.HandleFunc("GET /v1/transactions", s.listTransactions)
	mux.HandleFunc("POST /v1/transactions", s.createTransaction)
	mux.HandleFunc("GET /v1/transactions/{id}", s.getTransaction)
	mux.HandleFunc("POST /v1/transactions/{id}/{action}", s.followUp)
	mux.HandleFunc("GET /v1/events", s.events)

	mux.HandleFunc("GET /v1/stats", s.stats)
	mux.HandleFunc("GET /v1/stream", s.stream)
	mux.HandleFunc("POST /v1/audit/chain", s.verifyChain)
	mux.HandleFunc("POST /v1/audit/integrity", s.checkIntegrity)

	if s.opt.Demo {
		mux.HandleFunc("POST /v1/demo/seed", s.demoSeed)
		mux.HandleFunc("GET /v1/demo/load", s.demoLoadStatus)
		mux.HandleFunc("POST /v1/demo/load", s.demoLoadStart)
		mux.HandleFunc("DELETE /v1/demo/load", s.demoLoadStop)
		mux.HandleFunc("POST /v1/demo/tamper", s.demoTamper)
		mux.HandleFunc("DELETE /v1/demo/tamper", s.demoRestore)
		mux.HandleFunc("POST /v1/demo/reset", s.demoReset)
	}
	mux.Handle("/", spa())
	return s.guard(mux)
}

// guard applies security headers, the same-origin rule and the API key.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:")
		write := r.Method != http.MethodGet && r.Method != http.MethodHead
		if write {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
				writeErr(w, http.StatusForbidden, "forbidden", "origem não permitida")
				return
			}
			if s.opt.APIKey != "" {
				got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if subtle.ConstantTimeCompare([]byte(got), []byte(s.opt.APIKey)) != 1 {
					w.Header().Set("WWW-Authenticate", "Bearer")
					writeErr(w, http.StatusUnauthorized, "unauthorized", "chave de API ausente ou inválida")
					return
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.st.Ping(ctx); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "banco de dados indisponível")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"demo": s.opt.Demo, "maxBatch": s.p.MaxBatch(), "auth": s.opt.APIKey != "",
	})
}

// ---- accounts ------------------------------------------------------------

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.st.Accounts(r.Context(), q.Get("search"), q.Get("after"), limit(r, 50, 200))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": list})
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code          string             `json:"code"`
		Name          string             `json:"name"`
		Type          ledger.AccountType `json:"type"`
		Currency      string             `json:"currency"`
		AllowNegative *bool              `json:"allowNegative"`
		Metadata      map[string]string  `json:"metadata"`
	}
	if !decode(w, r, &in) {
		return
	}
	a := &ledger.Account{Code: in.Code, Name: in.Name, Type: in.Type, Currency: in.Currency, Metadata: in.Metadata}
	// By default, accounts that hold other people's money can't be overdrawn.
	a.AllowNegative = in.Type != ledger.Liability
	if in.AllowNegative != nil {
		a.AllowNegative = *in.AllowNegative
	}
	if err := s.st.CreateAccount(r.Context(), a); err != nil {
		fail(w, err)
		return
	}
	v, err := s.st.Account(r.Context(), a.ID.String())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	v, err := s.st.Account(r.Context(), r.PathValue("ref"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) statement(w http.ResponseWriter, r *http.Request) {
	v, err := s.st.Account(r.Context(), r.PathValue("ref"))
	if err != nil {
		fail(w, err)
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	lines, err := s.st.Statement(r.Context(), v.ID, before, limit(r, 50, 200))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": v, "lines": lines})
}

// ---- transactions --------------------------------------------------------

type postingIn struct {
	Account   string `json:"account"`
	Direction string `json:"direction"`
	Amount    int64  `json:"amount"`
}

type txIn struct {
	Description      string            `json:"description"`
	Metadata         map[string]string `json:"metadata"`
	Pending          bool              `json:"pending"`
	ExpiresInSeconds int64             `json:"expiresInSeconds"`
	Postings         []postingIn       `json:"postings"`
}

func direction(s string) ledger.Direction {
	switch strings.ToLower(s) {
	case "d", "debit", "débito", "debito":
		return ledger.Debit
	case "c", "credit", "crédito", "credito":
		return ledger.Credit
	}
	return ledger.Direction(s)
}

func (s *Server) createTransaction(w http.ResponseWriter, r *http.Request) {
	var in txIn
	if !decode(w, r, &in) {
		return
	}
	cmd := ledger.Command{
		Op: ledger.OpCreate, IdempotencyKey: r.Header.Get("Idempotency-Key"),
		Description: in.Description, Metadata: in.Metadata, Pending: in.Pending,
		ExpiresIn: time.Duration(in.ExpiresInSeconds) * time.Second,
	}
	if in.ExpiresInSeconds < 0 || in.ExpiresInSeconds > int64(ledger.MaxPendingTTL/time.Second) {
		writeErr(w, http.StatusBadRequest, string(ledger.ErrInvalid), "expiresInSeconds fora do intervalo")
		return
	}
	for _, p := range in.Postings {
		cmd.Postings = append(cmd.Postings, ledger.Posting{Account: p.Account, Direction: direction(p.Direction), Amount: p.Amount})
	}
	s.submit(w, r, cmd)
}

func (s *Server) followUp(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, string(ledger.ErrNotFound), "transação não encontrada")
		return
	}
	var in struct {
		Description string            `json:"description"`
		Metadata    map[string]string `json:"metadata"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	cmd := ledger.Command{IdempotencyKey: r.Header.Get("Idempotency-Key"), Target: id, Description: in.Description, Metadata: in.Metadata}
	switch r.PathValue("action") {
	case "post":
		cmd.Op = ledger.OpPost
	case "void":
		cmd.Op = ledger.OpVoid
	case "reverse":
		cmd.Op = ledger.OpReverse
	default:
		writeErr(w, http.StatusNotFound, string(ledger.ErrNotFound), "ação desconhecida: use post, void ou reverse")
		return
	}
	s.submit(w, r, cmd)
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request, cmd ledger.Command) {
	res := s.p.Submit(r.Context(), cmd)
	if res.Err != nil {
		fail(w, res.Err)
		return
	}
	code := http.StatusCreated
	if res.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		code = http.StatusOK
	}
	writeJSON(w, code, res.Tx)
}

func (s *Server) getTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, string(ledger.ErrNotFound), "transação não encontrada")
		return
	}
	tx, err := s.st.Transaction(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *Server) listTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	kind := q.Get("kind")
	switch ledger.Kind(kind) {
	case "", ledger.KindPosted, ledger.KindPending, ledger.KindPost, ledger.KindVoid:
	default:
		writeErr(w, http.StatusBadRequest, string(ledger.ErrInvalid), "kind inválido")
		return
	}
	txs, err := s.st.Transactions(r.Context(), before, kind, limit(r, 50, 200))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transactions": txs})
}

// events is the ordered feed for integrations: poll with the last seq seen.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	txs, err := s.st.Since(r.Context(), after, limit(r, 100, 1000))
	if err != nil {
		fail(w, err)
		return
	}
	next := after
	if len(txs) > 0 {
		next = txs[len(txs)-1].Seq
	}
	writeJSON(w, http.StatusOK, map[string]any{"transactions": txs, "next": next})
}

// ---- stats and audit -----------------------------------------------------

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	sum, err := s.st.Summary(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": s.p.Metrics.Snapshot(), "summary": sum})
}

// stream pushes writer metrics and the newest transactions over SSE.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal", "streaming não suportado")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	var lastSeq int64
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		s.mu.Lock()
		var fresh []*ledger.Transaction
		for _, t := range s.recent {
			if t.Seq > lastSeq {
				fresh = append(fresh, t)
			}
		}
		s.mu.Unlock()
		if n := len(fresh); n > 0 {
			lastSeq = fresh[n-1].Seq
			if n > 12 {
				fresh = fresh[n-12:]
			}
		}
		raw, _ := json.Marshal(map[string]any{"metrics": s.p.Metrics.Snapshot(), "recent": fresh, "load": s.loadReport.Load()})
		if _, err := w.Write([]byte("event: tick\ndata: " + string(raw) + "\n\n")); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Server) verifyChain(w http.ResponseWriter, r *http.Request) {
	rep, err := s.st.VerifyChain(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) checkIntegrity(w http.ResponseWriter, r *http.Request) {
	rep, err := s.st.CheckIntegrity(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// SweepExpired voids reservations whose deadline has passed. Voiding goes
// through the writer like any other command, so it is ordered and audited.
func (s *Server) SweepExpired(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		ids, err := s.st.ExpiredPending(ctx, 500)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("varredura de reservas expiradas falhou", "erro", err)
			}
			continue
		}
		for _, id := range ids {
			res := s.p.Submit(ctx, ledger.Command{
				Op: ledger.OpVoid, Target: id, IdempotencyKey: "expire-" + id.String(), Description: "reserva expirada",
			})
			if res.Err != nil && ledger.CodeOf(res.Err) != ledger.ErrAlreadyResolved && ctx.Err() == nil {
				slog.Warn("não foi possível cancelar reserva expirada", "id", id, "erro", res.Err)
			}
		}
	}
}

// ---- helpers -------------------------------------------------------------

func limit(r *http.Request, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 {
		return def
	}
	return min(n, max)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, string(ledger.ErrInvalid), "JSON inválido: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

var statusOf = map[ledger.Code]int{
	ledger.ErrInvalid:           http.StatusBadRequest,
	ledger.ErrUnbalanced:        http.StatusUnprocessableEntity,
	ledger.ErrAccountNotFound:   http.StatusNotFound,
	ledger.ErrInsufficientFunds: http.StatusUnprocessableEntity,
	ledger.ErrNotFound:          http.StatusNotFound,
	ledger.ErrNotPending:        http.StatusConflict,
	ledger.ErrAlreadyResolved:   http.StatusConflict,
	ledger.ErrAlreadyReversed:   http.StatusConflict,
	ledger.ErrNotReversible:     http.StatusConflict,
	ledger.ErrExpired:           http.StatusConflict,
	ledger.ErrIdempotency:       http.StatusConflict,
	ledger.ErrOverflow:          http.StatusUnprocessableEntity,
	ledger.ErrConflict:          http.StatusConflict,
	ledger.ErrUnavailable:       http.StatusServiceUnavailable,
}

// fail maps an error to a response. Business errors keep their code and
// message; anything else is logged and hidden from the client.
func fail(w http.ResponseWriter, err error) {
	var le *ledger.Error
	if errors.As(err, &le) {
		writeErr(w, statusOf[le.Code], string(le.Code), le.Message)
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	slog.Error("erro interno", "erro", err)
	writeErr(w, http.StatusInternalServerError, "internal", "erro interno")
}

// spa serves the embedded frontend with a fallback to index.html.
func spa() http.Handler {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasPrefix(p, "v1/") {
			writeErr(w, http.StatusNotFound, "not_found", "rota não encontrada")
			return
		}
		if p != "" {
			if _, err := fs.Stat(dist, p); err == nil {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "frontend não compilado: rode .\\run.ps1", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}
