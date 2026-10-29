ALTER TABLE ride_requests ALTER COLUMN class_start TYPE TIME USING class_start::time;
ALTER TABLE ride_requests DROP COLUMN outcome;
