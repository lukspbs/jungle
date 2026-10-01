-- A reversão é lossy por natureza: voltar para JSONB normaliza os payloads
-- existentes e a ordem original das chaves não é recuperável.
ALTER TABLE outbox_events
    ALTER COLUMN payload TYPE JSONB USING payload::text::jsonb;
