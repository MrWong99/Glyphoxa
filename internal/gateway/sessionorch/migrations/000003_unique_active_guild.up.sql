-- Prevent two active sessions for the same guild (defense-in-depth alongside
-- the in-memory sentinel in GatewaySessionController.Start).
CREATE UNIQUE INDEX IF NOT EXISTS idx_one_active_session_per_guild
    ON sessions (guild_id)
    WHERE state != 'ended';
