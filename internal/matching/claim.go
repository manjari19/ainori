package matching

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"ainori/internal/location"
)

// claimScript checks seats and takes one in a single atomic step. Redis runs a Lua
// script start to finish before any other command, so two riders can never both see
// "1 seat left" and both take it. A missing hash means the driver went stale: no claim.
// It returns {seats left after the claim, seats total}, or {-1, 0} if the claim failed.
var claimScript = redis.NewScript(`
local seats = redis.call('HGET', KEYS[1], 'seats_left')
if not seats or tonumber(seats) <= 0 then
  return {-1, 0}
end
local total = redis.call('HGET', KEYS[1], 'seats_total') or seats
return {redis.call('HINCRBY', KEYS[1], 'seats_left', -1), tonumber(total)}
`)

// releaseScript hands a seat back, but only if the driver still exists.
var releaseScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  return redis.call('HINCRBY', KEYS[1], 'seats_left', 1)
end
return -1
`)

// ClaimSeat tries to take one seat. ok is false if the car is full or the driver is gone.
// seatsTotal lets the caller work out how many riders are on the trip for the cost split.
func ClaimSeat(ctx context.Context, rdb *redis.Client, driverID string) (seatsLeft, seatsTotal int, ok bool, err error) {
	res, err := claimScript.Run(ctx, rdb, []string{location.DriverKey(driverID)}).Int64Slice()
	if err != nil {
		return 0, 0, false, fmt.Errorf("claim seat for %s: %w", driverID, err)
	}
	if len(res) != 2 || res[0] < 0 {
		return 0, 0, false, nil
	}
	return int(res[0]), int(res[1]), true, nil
}

// ReleaseSeat undoes a claim, used when saving the trip fails after the seat was taken.
func ReleaseSeat(ctx context.Context, rdb *redis.Client, driverID string) error {
	if err := releaseScript.Run(ctx, rdb, []string{location.DriverKey(driverID)}).Err(); err != nil {
		return fmt.Errorf("release seat for %s: %w", driverID, err)
	}
	return nil
}
