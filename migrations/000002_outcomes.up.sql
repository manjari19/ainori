-- A request's outcome is stored whole so a redelivered message can re-publish
-- the same event instead of matching again.
ALTER TABLE ride_requests ADD COLUMN outcome JSONB;
ALTER TABLE ride_requests
    ALTER COLUMN class_start TYPE TIMESTAMPTZ USING (current_date + class_start);
