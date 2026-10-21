-- Applications are run from the console end to end: a description for the
-- people asking for access, how often Grantra reads them (0 = only when
-- someone asks), and retirement. A retired application keeps its history and
-- the access people still hold, but takes no new access and is no longer read
-- or written to.
ALTER TABLE applications
    ADD COLUMN description      text,
    ADD COLUMN read_every_hours integer NOT NULL DEFAULT 1 CHECK (read_every_hours BETWEEN 0 AND 168),
    ADD COLUMN retired_at       timestamptz;
