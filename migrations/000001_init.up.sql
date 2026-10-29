CREATE TABLE drivers (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             TEXT NOT NULL,
    gender           TEXT NOT NULL CHECK (gender IN ('woman', 'man', 'nonbinary')),
    seats_total      INT  NOT NULL CHECK (seats_total > 0),
    route_id         TEXT NOT NULL,
    departure_time   TIMESTAMPTZ,
    fuel_l_per_100km DOUBLE PRECISION NOT NULL DEFAULT 8.0,
    women_nb_only    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE riders (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT NOT NULL,
    gender        TEXT NOT NULL CHECK (gender IN ('woman', 'man', 'nonbinary')),
    pickup_lat    DOUBLE PRECISION NOT NULL,
    pickup_lng    DOUBLE PRECISION NOT NULL,
    class_start   TIME NOT NULL,
    women_nb_only BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE trips (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id       UUID NOT NULL REFERENCES drivers(id),
    rider_ids       UUID[] NOT NULL DEFAULT '{}',
    matched_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    pickup_points   JSONB NOT NULL DEFAULT '[]',
    est_arrival     TIMESTAMPTZ NOT NULL,
    distance_km     DOUBLE PRECISION NOT NULL,
    cost_per_person NUMERIC(8, 2) NOT NULL
);

-- The request id is the idempotency key: a request points at most one trip, so
-- redelivering the same stream message cannot create a second trip.
CREATE TABLE ride_requests (
    id          UUID PRIMARY KEY,
    rider_id    UUID NOT NULL REFERENCES riders(id),
    pickup_lat  DOUBLE PRECISION NOT NULL,
    pickup_lng  DOUBLE PRECISION NOT NULL,
    class_start TIME NOT NULL,
    status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'matched', 'unmatched')),
    trip_id     UUID REFERENCES trips(id),
    reason      TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE recurring_rides (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id    UUID NOT NULL REFERENCES drivers(id),
    rider_id     UUID NOT NULL REFERENCES riders(id),
    days_of_week TEXT[] NOT NULL,
    class_start  TIME NOT NULL,
    active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX trips_driver_idx ON trips (driver_id);
CREATE INDEX ride_requests_rider_idx ON ride_requests (rider_id);
CREATE INDEX recurring_active_idx ON recurring_rides (active) WHERE active;
