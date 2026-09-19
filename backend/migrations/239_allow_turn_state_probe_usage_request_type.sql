-- Turn-state hunter probes are separately classified from user traffic.
-- Keep existing request_type values and extend the bounded enum with 6.
ALTER TABLE usage_logs DROP CONSTRAINT IF EXISTS usage_logs_request_type_check;
ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_request_type_check CHECK (request_type >= 0 AND request_type <= 6) NOT VALID;
