// Package pipeline funnels every write through a single writer that commits
// in groups.
//
// There is no timer: while one batch is being committed, incoming commands
// queue up and become the next batch. Under light load a command is its own
// batch and pays one commit; under heavy load hundreds share one commit, so
// throughput grows with pressure instead of collapsing.
package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"razao/internal/ledger"
)

// Executor commits a batch atomically.
type Executor interface {
	ExecBatch(ctx context.Context, cmds []ledger.Command) ([]ledger.Result, error)
}

type request struct {
	cmd   ledger.Command
	reply chan ledger.Result
	at    time.Time
}

// Pipeline is the single writer.
type Pipeline struct {
	exec     Executor
	in       chan *request
	maxBatch int
	Metrics  *Metrics
	// OnCommit, if set, is called with each batch's new transactions after
	// they are durable, from the writer goroutine.
	OnCommit func([]*ledger.Transaction)
}

// New builds a pipeline. maxBatch of 1 disables grouping: one database
// transaction per command, which is the baseline the benchmark compares to.
func New(exec Executor, maxBatch int) *Pipeline {
	if maxBatch < 1 {
		maxBatch = 1
	}
	return &Pipeline{exec: exec, in: make(chan *request, 4*maxBatch+1024), maxBatch: maxBatch, Metrics: NewMetrics()}
}

// MaxBatch reports the configured batch limit.
func (p *Pipeline) MaxBatch() int { return p.maxBatch }

// Submit queues a command and waits for its outcome.
func (p *Pipeline) Submit(ctx context.Context, cmd ledger.Command) ledger.Result {
	r := &request{cmd: cmd, reply: make(chan ledger.Result, 1), at: time.Now()}
	select {
	case p.in <- r:
	case <-ctx.Done():
		return ledger.Result{Err: ledger.Errorf(ledger.ErrUnavailable, "requisição cancelada antes de entrar na fila")}
	}
	select {
	case res := <-r.reply:
		return res
	case <-ctx.Done():
		// The command may still commit. The caller can retry with the same
		// idempotency key to learn the outcome.
		return ledger.Result{Err: ledger.Errorf(ledger.ErrUnavailable, "requisição cancelada; repita com a mesma Idempotency-Key")}
	}
}

// Run is the writer loop. It returns when ctx is cancelled.
func (p *Pipeline) Run(ctx context.Context) {
	batch := make([]*request, 0, p.maxBatch)
	cmds := make([]ledger.Command, 0, p.maxBatch)
	for {
		batch, cmds = batch[:0], cmds[:0]
		select {
		case <-ctx.Done():
			return
		case r := <-p.in:
			batch = append(batch, r)
		}
	drain:
		for len(batch) < p.maxBatch {
			select {
			case r := <-p.in:
				batch = append(batch, r)
			default:
				break drain
			}
		}
		for _, r := range batch {
			cmds = append(cmds, r.cmd)
		}

		start := time.Now()
		results, err := p.execWithRetry(ctx, cmds)
		took := time.Since(start)
		if err != nil {
			slog.Error("lote falhou", "tamanho", len(batch), "erro", err)
			p.Metrics.batchFailed(len(batch))
			for _, r := range batch {
				r.reply <- ledger.Result{Err: ledger.Errorf(ledger.ErrUnavailable, "o diário está indisponível, tente de novo")}
			}
			continue
		}
		now := time.Now()
		var created []*ledger.Transaction
		committed, rejected, replayed := 0, 0, 0
		for i, r := range batch {
			res := results[i]
			switch {
			case res.Err != nil:
				rejected++
			case res.Replayed:
				replayed++
			default:
				committed++
				created = append(created, res.Tx)
			}
			p.Metrics.latency(now.Sub(r.at))
			r.reply <- res
		}
		p.Metrics.batchDone(now, len(batch), committed, rejected, replayed, took)
		if p.OnCommit != nil && len(created) > 0 {
			p.OnCommit(created)
		}
	}
}

// execWithRetry retries infrastructure failures. A failed batch committed
// nothing, and every command carries an idempotency key, so retrying is safe.
func (p *Pipeline) execWithRetry(ctx context.Context, cmds []ledger.Command) ([]ledger.Result, error) {
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
			}
		}
		var res []ledger.Result
		// The batch must not be abandoned halfway by one caller going away.
		res, err = p.exec.ExecBatch(context.WithoutCancel(ctx), cmds)
		if err == nil {
			return res, nil
		}
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
	}
	return nil, err
}
