CREATE TABLE IF NOT EXISTS usage_records (
    id             BIGSERIAL    PRIMARY KEY,
    tenant_id      TEXT         NOT NULL,
    period         DATE         NOT NULL,  -- first of month
    session_hours  NUMERIC(10,2) NOT NULL DEFAULT 0,
    llm_tokens     BIGINT       NOT NULL DEFAULT 0,
    stt_seconds    NUMERIC(10,2) NOT NULL DEFAULT 0,
    tts_chars      BIGINT       NOT NULL DEFAULT 0,
    UNIQUE(tenant_id, period)
);

CREATE INDEX IF NOT EXISTS idx_usage_records_tenant ON usage_records (tenant_id);
