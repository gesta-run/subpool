ALTER TABLE api_key_usage_daily ADD COLUMN model text;
UPDATE api_key_usage_daily SET model = 'unknown' WHERE model IS NULL;
ALTER TABLE api_key_usage_daily ALTER COLUMN model SET NOT NULL;
ALTER TABLE api_key_usage_daily ALTER COLUMN model SET DEFAULT 'unknown';
ALTER TABLE api_key_usage_daily DROP CONSTRAINT api_key_usage_daily_pkey;
ALTER TABLE api_key_usage_daily ADD PRIMARY KEY (api_key_id, usage_date, model);
