-- Plano de contas. Imutável depois de criado: o saldo fica em outra tabela.
CREATE TABLE accounts (
    id             uuid PRIMARY KEY,
    code           text NOT NULL UNIQUE CHECK (code ~ '^[a-z0-9][a-z0-9:_.-]{0,95}$'),
    name           text NOT NULL,
    type           text NOT NULL CHECK (type IN ('asset', 'liability', 'equity', 'revenue', 'expense')),
    currency       text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    allow_negative boolean NOT NULL,
    metadata       jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL
);

-- Saldos materializados. Linhas estreitas e quentes: fillfactor baixo
-- deixa espaço para HOT updates.
CREATE TABLE balances (
    account_id      uuid PRIMARY KEY REFERENCES accounts (id),
    debits_posted   bigint NOT NULL DEFAULT 0 CHECK (debits_posted >= 0),
    credits_posted  bigint NOT NULL DEFAULT 0 CHECK (credits_posted >= 0),
    debits_pending  bigint NOT NULL DEFAULT 0 CHECK (debits_pending >= 0),
    credits_pending bigint NOT NULL DEFAULT 0 CHECK (credits_pending >= 0),
    entries         bigint NOT NULL DEFAULT 0,
    updated_seq     bigint NOT NULL DEFAULT 0
) WITH (fillfactor = 70);

-- O diário. Só recebe INSERT. seq é a ordem total de commit e cada linha
-- carrega o hash da anterior.
CREATE TABLE transactions (
    seq             bigint PRIMARY KEY,
    id              uuid NOT NULL UNIQUE,
    kind            text NOT NULL CHECK (kind IN ('posted', 'pending', 'post', 'void')),
    related_id      uuid REFERENCES transactions (id),
    reversal        boolean NOT NULL DEFAULT false,
    idempotency_key text NOT NULL UNIQUE,
    request_hash    bytea NOT NULL,
    description     text NOT NULL DEFAULT '',
    metadata        jsonb NOT NULL DEFAULT '{}',
    expires_at      timestamptz,
    created_at      timestamptz NOT NULL,
    prev_hash       bytea NOT NULL,
    hash            bytea NOT NULL,
    CHECK ((kind IN ('post', 'void')) <= (related_id IS NOT NULL)),
    CHECK (reversal <= (related_id IS NOT NULL AND kind = 'posted'))
);

-- Uma reserva só pode ser resolvida uma vez; uma transação só pode ser estornada uma vez.
CREATE UNIQUE INDEX transactions_one_resolution ON transactions (related_id) WHERE kind IN ('post', 'void');
CREATE UNIQUE INDEX transactions_one_reversal ON transactions (related_id) WHERE reversal;
-- Quem resolve ou estorna uma transação: consultado a cada lote com capturas, cancelamentos ou replays.
CREATE INDEX transactions_related ON transactions (related_id) WHERE related_id IS NOT NULL;
CREATE INDEX transactions_pending_expiry ON transactions (expires_at) WHERE kind = 'pending' AND expires_at IS NOT NULL;

-- Partidas. Sem chaves estrangeiras de propósito: a verificação por linha
-- custava ~85% do tempo de gravação de um lote (medido: 45 ms -> 4 ms para
-- 600 partidas). Quem garante a referência é o escritor único, que só grava
-- partidas de transações do mesmo lote e de contas que validou; o
-- verificador de integridade procura partidas órfãs.
CREATE TABLE entries (
    tx_seq        bigint NOT NULL,
    idx           smallint NOT NULL,
    account_id    uuid NOT NULL,
    direction     text NOT NULL CHECK (direction IN ('D', 'C')),
    amount        bigint NOT NULL CHECK (amount > 0),
    -- saldo contabilizado da conta (no lado natural) depois desta partida;
    -- nulo quando a partida só mexe em valores pendentes.
    balance_after bigint,
    PRIMARY KEY (tx_seq, idx)
);
CREATE INDEX entries_by_account ON entries (account_id, tx_seq DESC, idx DESC);

-- Cabeça da cadeia. Travar esta linha é o que serializa os escritores.
CREATE TABLE ledger_head (
    id   boolean PRIMARY KEY DEFAULT true CHECK (id),
    seq  bigint NOT NULL,
    hash bytea NOT NULL
);
INSERT INTO ledger_head (seq, hash) VALUES (0, decode(repeat('00', 32), 'hex'));

-- O diário é imutável também para quem tem acesso SQL comum.
CREATE FUNCTION razao_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'razao: % em % não é permitido, o diário é imutável', TG_OP, TG_TABLE_NAME;
END $$;
CREATE TRIGGER transactions_immutable BEFORE UPDATE OR DELETE ON transactions
    FOR EACH ROW EXECUTE FUNCTION razao_immutable();
CREATE TRIGGER entries_immutable BEFORE UPDATE OR DELETE ON entries
    FOR EACH ROW EXECUTE FUNCTION razao_immutable();
CREATE TRIGGER accounts_immutable BEFORE UPDATE OR DELETE ON accounts
    FOR EACH ROW EXECUTE FUNCTION razao_immutable();
