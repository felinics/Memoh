-- 0120_discuss_event_cursor
-- Drop the discuss event-cursor column and the session event cursor sequence.

ALTER TABLE bot_session_discuss_cursors
  DROP COLUMN IF EXISTS consumed_event_cursor;

DROP SEQUENCE IF EXISTS bot_session_event_cursor_seq;
