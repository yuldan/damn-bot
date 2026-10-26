-- 0013_events_turn.sql -- pencarian turn lintas sesi (`sessions why <turn-id>`,
-- SPEC §10/§4.2). `idx_events_turn (session_id, turn_id)` dari 0001 tidak
-- melayani `WHERE turn_id = ?` tanpa session_id, jadi helper TurnSessions
-- memakai indeks global ini. Partial: turn_id boleh NULL (event sistem).

CREATE INDEX idx_events_turn_id ON events(turn_id) WHERE turn_id IS NOT NULL;
