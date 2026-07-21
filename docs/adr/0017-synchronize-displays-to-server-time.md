# Synchronize Displays to server time

The Venue server clock is authoritative for live operation.
Displays estimate their offset from server time, advance timers with a local monotonic clock, and resynchronize periodically and after reconnection instead of trusting their own wall clocks.
An offline Display continues from its last known offset, while the crew dashboard flags excessive skew or unstable synchronization.
