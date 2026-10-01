package api

import (
	"context"
	"math/rand/v2"
	"net/http"
	"time"

	"razao/internal/demo"
	"razao/internal/ledger"
	"razao/internal/loadgen"
	"razao/internal/store"
)

func (s *Server) demoSeed(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Wallets int `json:"wallets"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if in.Wallets == 0 {
		in.Wallets = 200
	}
	if in.Wallets < 2 || in.Wallets > 100_000 {
		writeErr(w, http.StatusBadRequest, string(ledger.ErrInvalid), "wallets deve estar entre 2 e 100000")
		return
	}
	codes, err := demo.Seed(r.Context(), s.st, s.p.Submit, in.Wallets)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"wallets": len(codes)})
}

func (s *Server) demoLoadStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"load": s.loadReport.Load()})
}

func (s *Server) demoLoadStart(w http.ResponseWriter, r *http.Request) {
	in := struct {
		Workers  int     `json:"workers"`
		Seconds  int     `json:"seconds"`
		Wallets  int     `json:"wallets"`
		HotShare float64 `json:"hotShare"`
	}{Workers: 128, Seconds: 15, Wallets: 500, HotShare: 0.2}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if in.Workers < 1 || in.Workers > 2048 || in.Seconds < 1 || in.Seconds > 300 ||
		in.Wallets < 2 || in.Wallets > 100_000 || in.HotShare < 0 || in.HotShare > 1 {
		writeErr(w, http.StatusBadRequest, string(ledger.ErrInvalid),
			"workers 1–2048, seconds 1–300, wallets 2–100000, hotShare 0–1")
		return
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if rep := s.loadReport.Load(); rep != nil && rep.Running {
		writeErr(w, http.StatusConflict, string(ledger.ErrConflict), "já existe um teste de carga rodando")
		return
	}
	codes, err := demo.Seed(r.Context(), s.st, s.p.Submit, in.Wallets)
	if err != nil {
		fail(w, err)
		return
	}
	cfg := loadgen.Config{
		Workers: in.Workers, Seconds: in.Seconds, Duration: time.Duration(in.Seconds) * time.Second,
		Wallets: codes, Funding: demo.Bank, Fees: demo.Fees, HotShare: in.HotShare, HoldShare: 0.15,
	}
	// The run outlives this request.
	ctx, cancel := context.WithCancel(context.Background())
	s.loadCancel = cancel
	s.p.Metrics.ResetLatency()
	s.loadReport.Store(&loadgen.Report{Running: true, Config: cfg, StartedAt: time.Now(), Reasons: map[string]int64{}})
	go func() {
		defer cancel()
		loadgen.Run(ctx, cfg, s.p.Submit, &s.loadReport)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"load": s.loadReport.Load()})
}

func (s *Server) demoLoadStop(w http.ResponseWriter, _ *http.Request) {
	s.loadMu.Lock()
	if s.loadCancel != nil {
		s.loadCancel()
	}
	s.loadMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// demoTamper changes one stored amount behind the ledger's back.
func (s *Server) demoTamper(w http.ResponseWriter, r *http.Request) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if s.tampered != nil {
		writeErr(w, http.StatusConflict, string(ledger.ErrConflict), "o diário já está adulterado; restaure antes")
		return
	}
	sum, err := s.st.Summary(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if sum.Seq < 1 {
		writeErr(w, http.StatusConflict, string(ledger.ErrConflict), "o diário está vazio: crie transações primeiro")
		return
	}
	// Somewhere in the middle, where nobody would look.
	t := store.Tampering{Seq: 1 + rand.Int64N(sum.Seq), Idx: 0, Delta: 100_000}
	if err := s.st.Tamper(r.Context(), t); err != nil {
		fail(w, err)
		return
	}
	s.tampered = &t
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) demoRestore(w http.ResponseWriter, r *http.Request) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if s.tampered == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	undo := *s.tampered
	undo.Delta = -undo.Delta
	if err := s.st.Tamper(r.Context(), undo); err != nil {
		fail(w, err)
		return
	}
	s.tampered = nil
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) demoReset(w http.ResponseWriter, r *http.Request) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if rep := s.loadReport.Load(); rep != nil && rep.Running {
		writeErr(w, http.StatusConflict, string(ledger.ErrConflict), "pare o teste de carga antes de zerar")
		return
	}
	if err := s.st.Reset(r.Context()); err != nil {
		fail(w, err)
		return
	}
	s.tampered = nil
	s.loadReport.Store(nil)
	s.mu.Lock()
	s.recent = nil
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "zerado"})
}
