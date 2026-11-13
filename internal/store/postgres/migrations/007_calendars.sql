-- Published calendar feeds (iCloud "Public Calendar" links, Google's secret
-- address) shown in the calendar panel. Each link is a credential: anyone who
-- has it reads that calendar, so the API never sends them back.
ALTER TABLE settings
    ADD COLUMN calendars text[] NOT NULL DEFAULT '{}'
        CHECK (cardinality(calendars) <= 4);
