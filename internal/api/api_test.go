package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"razao/internal/api"
	"razao/internal/ledger"
	"razao/internal/pipeline"
	"razao/internal/testdb"
)

type client struct {
	t   *testing.T
	url string
	key string
}

func start(t *testing.T, opt api.Options) *client {
	t.Helper()
	st := testdb.Open(t, "api")
	ctx, cancel := context.WithCancel(context.Background())
	p := pipeline.New(st, 500)
	go p.Run(ctx)
	srv := httptest.NewServer(api.New(st, p, opt).Handler())
	t.Cleanup(func() { srv.Close(); cancel() })
	return &client{t: t, url: srv.URL, key: opt.APIKey}
}

// do sends a request and decodes the JSON response into out.
func (c *client) do(method, path, idem, body string, out any) (int, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.url+path, strings.NewReader(body))
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			c.t.Fatalf("%s %s: resposta não é JSON: %v", method, path, err)
		}
	}
	return res.StatusCode, res.Header
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *client) accounts() {
	c.t.Helper()
	for _, body := range []string{
		`{"code":"bank","name":"Banco","type":"asset","currency":"BRL"}`,
		`{"code":"alice","name":"Alice","type":"liability","currency":"BRL"}`,
		`{"code":"bob","name":"Bob","type":"liability","currency":"BRL"}`,
	} {
		if code, _ := c.do("POST", "/v1/accounts", "", body, nil); code != 201 {
			c.t.Fatalf("criar conta: %d", code)
		}
	}
}

const deposit = `{"description":"depósito","postings":[{"account":"bank","direction":"debit","amount":1000},{"account":"alice","direction":"credit","amount":1000}]}`

func TestTransactionLifecycle(t *testing.T) {
	c := start(t, api.Options{})
	c.accounts()

	var tx ledger.Transaction
	code, hdr := c.do("POST", "/v1/transactions", "dep-1", deposit, &tx)
	if code != 201 || tx.Seq != 1 || len(tx.Entries) != 2 || hdr.Get("Idempotent-Replayed") != "" {
		t.Fatalf("criar: %d %+v", code, tx)
	}
	var again ledger.Transaction
	code, hdr = c.do("POST", "/v1/transactions", "dep-1", deposit, &again)
	if code != 200 || hdr.Get("Idempotent-Replayed") != "true" || again.ID != tx.ID {
		t.Fatalf("replay: %d %q", code, hdr.Get("Idempotent-Replayed"))
	}

	var acct struct {
		Posted    int64 `json:"posted"`
		Available int64 `json:"available"`
		AllowNeg  bool  `json:"allowNegative"`
	}
	c.do("GET", "/v1/accounts/alice", "", "", &acct)
	if acct.Posted != 1000 || acct.AllowNeg {
		t.Fatalf("alice = %+v (carteiras não podem ficar negativas por padrão)", acct)
	}

	// Reserve, then capture.
	var hold ledger.Transaction
	c.do("POST", "/v1/transactions", "hold-1",
		`{"pending":true,"expiresInSeconds":600,"postings":[{"account":"alice","direction":"D","amount":400},{"account":"bob","direction":"C","amount":400}]}`, &hold)
	c.do("GET", "/v1/accounts/alice", "", "", &acct)
	if hold.Kind != ledger.KindPending || hold.ExpiresAt == nil || acct.Available != 600 {
		t.Fatalf("reserva: %+v, alice %+v", hold, acct)
	}
	if code, _ := c.do("POST", "/v1/transactions/"+hold.ID.String()+"/post", "cap-1", "", nil); code != 201 {
		t.Fatalf("captura: %d", code)
	}
	var e apiError
	if code, _ := c.do("POST", "/v1/transactions/"+hold.ID.String()+"/void", "void-1", "", &e); code != 409 || e.Error.Code != "already_resolved" {
		t.Fatalf("cancelar reserva capturada: %d %+v", code, e)
	}

	// Reversing the first deposit would overdraw Alice, who already spent part of it.
	if code, _ := c.do("POST", "/v1/transactions/"+tx.ID.String()+"/reverse", "rev-0", "", &e); code != 422 || e.Error.Code != "insufficient_funds" {
		t.Fatalf("estorno sem saldo: %d %+v", code, e)
	}
	var dep2 ledger.Transaction
	c.do("POST", "/v1/transactions", "dep-2", strings.Replace(deposit, "1000", "500", 2), &dep2)
	if code, _ := c.do("POST", "/v1/transactions/"+dep2.ID.String()+"/reverse", "rev-1", `{"description":"estorno"}`, nil); code != 201 {
		t.Fatalf("estorno: %d", code)
	}
	if code, _ := c.do("POST", "/v1/transactions/"+dep2.ID.String()+"/reverse", "rev-2", "", &e); code != 409 || e.Error.Code != "already_reversed" {
		t.Fatalf("segundo estorno: %d %+v", code, e)
	}

	// Newest first: reversal 600, deposit 1100, capture 600, hold (no settled effect), deposit 1000.
	var stmt struct {
		Lines []struct {
			BalanceAfter *int64 `json:"balanceAfter"`
		} `json:"lines"`
	}
	c.do("GET", "/v1/accounts/alice/statement", "", "", &stmt)
	want := []int64{600, 1100, 600, -1, 1000}
	if len(stmt.Lines) != len(want) {
		t.Fatalf("extrato com %d linhas, queria %d", len(stmt.Lines), len(want))
	}
	for i, l := range stmt.Lines {
		if (want[i] < 0) != (l.BalanceAfter == nil) || (l.BalanceAfter != nil && *l.BalanceAfter != want[i]) {
			t.Fatalf("extrato linha %d: saldo %v, queria %d", i, l.BalanceAfter, want[i])
		}
	}

	var feed struct {
		Transactions []ledger.Transaction `json:"transactions"`
		Next         int64                `json:"next"`
	}
	c.do("GET", "/v1/events?after=1", "", "", &feed)
	if len(feed.Transactions) < 2 || feed.Transactions[0].Seq != 2 || feed.Next != feed.Transactions[len(feed.Transactions)-1].Seq {
		t.Fatalf("feed: %d transações, next %d", len(feed.Transactions), feed.Next)
	}

	var chain struct{ OK bool }
	var integ struct{ OK bool }
	c.do("POST", "/v1/audit/chain", "", "", &chain)
	c.do("POST", "/v1/audit/integrity", "", "", &integ)
	if !chain.OK || !integ.OK {
		t.Fatalf("auditoria: cadeia %v integridade %v", chain.OK, integ.OK)
	}
}

func TestErrors(t *testing.T) {
	c := start(t, api.Options{})
	c.accounts()
	cases := []struct {
		name, idem, body string
		status           int
		code             string
	}{
		{"sem chave de idempotência", "", deposit, 400, "invalid_request"},
		{"não fecha", "e1", `{"postings":[{"account":"bank","direction":"D","amount":10},{"account":"alice","direction":"C","amount":9}]}`, 422, "unbalanced"},
		{"conta inexistente", "e2", `{"postings":[{"account":"bank","direction":"D","amount":10},{"account":"ghost","direction":"C","amount":10}]}`, 404, "account_not_found"},
		{"sem saldo", "e3", `{"postings":[{"account":"alice","direction":"D","amount":10},{"account":"bob","direction":"C","amount":10}]}`, 422, "insufficient_funds"},
		{"campo desconhecido", "e4", `{"postings":[],"hack":1}`, 400, "invalid_request"},
		{"valor negativo", "e5", `{"postings":[{"account":"bank","direction":"D","amount":-5},{"account":"alice","direction":"C","amount":-5}]}`, 400, "invalid_request"},
	}
	for _, tc := range cases {
		var e apiError
		if status, _ := c.do("POST", "/v1/transactions", tc.idem, tc.body, &e); status != tc.status || e.Error.Code != tc.code {
			t.Errorf("%s: %d %q, queria %d %q", tc.name, status, e.Error.Code, tc.status, tc.code)
		}
	}

	c.do("POST", "/v1/transactions", "same", deposit, nil)
	var e apiError
	other := strings.Replace(deposit, "1000", "2000", 2)
	if status, _ := c.do("POST", "/v1/transactions", "same", other, &e); status != 409 || e.Error.Code != "idempotency_conflict" {
		t.Errorf("chave reutilizada: %d %q", status, e.Error.Code)
	}
	if status, _ := c.do("POST", "/v1/accounts", "", `{"code":"bank","name":"x","type":"asset","currency":"BRL"}`, &e); status != 409 {
		t.Errorf("conta duplicada: %d", status)
	}
	if status, _ := c.do("POST", "/v1/accounts", "", `{"code":"Bad Code","name":"x","type":"asset","currency":"BRL"}`, &e); status != 400 {
		t.Errorf("código inválido: %d", status)
	}
	if status, _ := c.do("GET", "/v1/transactions/not-a-uuid", "", "", &e); status != 404 {
		t.Errorf("id inválido: %d", status)
	}
	// The demo routes don't exist unless enabled.
	if status, _ := c.do("POST", "/v1/demo/reset", "", "", &e); status != 404 {
		t.Errorf("rota de demo sem -demo: %d", status)
	}
}

func TestAuthAndOrigin(t *testing.T) {
	c := start(t, api.Options{APIKey: "s3cret"})
	c.accounts() // sends the key

	anon := &client{t: t, url: c.url}
	var e apiError
	if status, _ := anon.do("POST", "/v1/transactions", "k", deposit, &e); status != 401 {
		t.Fatalf("escrita sem chave: %d", status)
	}
	if status, _ := anon.do("GET", "/v1/accounts", "", "", nil); status != 200 {
		t.Fatalf("leitura sem chave: %d", status)
	}

	req, _ := http.NewRequest("POST", c.url+"/v1/transactions", strings.NewReader(deposit))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Idempotency-Key", "x")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("escrita de outra origem: %d", res.StatusCode)
	}
}

func TestStreamAndDemo(t *testing.T) {
	c := start(t, api.Options{Demo: true})
	var seeded struct{ Wallets int }
	if status, _ := c.do("POST", "/v1/demo/seed", "", `{"wallets":20}`, &seeded); status != 200 || seeded.Wallets != 20 {
		t.Fatalf("seed: %d %+v", status, seeded)
	}
	if status, _ := c.do("POST", "/v1/demo/load", "", `{"workers":16,"seconds":1,"wallets":20,"hotShare":0.5}`, nil); status != 202 {
		t.Fatalf("iniciar carga: %d", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.url+"/v1/stream", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	var last struct {
		Metrics pipeline.Snapshot `json:"metrics"`
		Load    *struct {
			Running   bool  `json:"running"`
			Committed int64 `json:"committed"`
		} `json:"load"`
	}
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if err := json.Unmarshal([]byte(data), &last); err != nil {
			t.Fatal(err)
		}
		if last.Load != nil && !last.Load.Running {
			break
		}
	}
	if last.Load == nil || last.Load.Committed == 0 || last.Metrics.Committed == 0 {
		t.Fatalf("o stream não mostrou a carga: %+v", last)
	}

	var tam struct{ Seq int64 }
	c.do("POST", "/v1/demo/tamper", "", "", &tam)
	var chain struct {
		OK       bool  `json:"ok"`
		BrokenAt int64 `json:"brokenAt"`
	}
	c.do("POST", "/v1/audit/chain", "", "", &chain)
	if chain.OK || chain.BrokenAt != tam.Seq {
		t.Fatalf("adulteração em %d, cadeia %+v", tam.Seq, chain)
	}
	c.do("DELETE", "/v1/demo/tamper", "", "", nil)
	c.do("POST", "/v1/audit/chain", "", "", &chain)
	if !chain.OK {
		t.Fatal("cadeia continua quebrada depois de restaurar")
	}
}
