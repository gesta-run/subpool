ALTER TABLE pools
    DROP CONSTRAINT IF EXISTS pools_provider_check;

ALTER TABLE pools
    ADD CONSTRAINT pools_provider_check
    CHECK (provider IN ('codex', 'openai_compatible', 'mixed'));
