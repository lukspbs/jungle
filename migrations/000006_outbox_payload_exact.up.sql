-- O payload da outbox passa de JSONB para JSON.
--
-- JSONB é um formato binário normalizado: ele reordena as chaves do objeto e
-- descarta a formatação original. O conteúdo semântico sobrevive, os bytes não.
--
-- Para a outbox isso é o tipo errado. O payload é um instantâneo imutável do
-- commit, publicado verbatim: uma republicação precisa entregar exatamente os
-- mesmos bytes da primeira tentativa, de modo que um consumidor que verifique
-- assinatura ou hash sobre o payload chegue ao mesmo resultado nas duas.
--
-- JSON guarda o texto como recebido e ainda valida a boa formação. Não há
-- perda: nunca consulto dentro do payload, então os operadores e índices
-- que só o JSONB oferece não fazem falta aqui.

ALTER TABLE outbox_events
    ALTER COLUMN payload TYPE JSON USING payload::text::json;
