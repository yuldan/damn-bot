DROP TRIGGER events_ai;
CREATE TRIGGER events_ai AFTER INSERT ON events
WHEN NEW.kind IN ('user_message','assistant_message','tool_result','imported')
BEGIN
    INSERT INTO events_fts(rowid, content, tool_name)
    VALUES (NEW.id, dmb_fts_norm(coalesce(NEW.content_enc, NEW.content), 'events.content', NEW.session_id || '|' || NEW.seq), coalesce(NEW.tool_name, ''));
END;
DROP TRIGGER summaries_ai;
CREATE TRIGGER summaries_ai AFTER INSERT ON summaries BEGIN
    INSERT INTO summaries_fts(rowid, content)
    VALUES (NEW.id, dmb_fts_norm(coalesce(NEW.content_enc, NEW.content), 'summaries.content', NEW.session_id || '|' || NEW.from_seq || ':' || NEW.to_seq));
END;
DROP TRIGGER memory_ai;
CREATE TRIGGER memory_ai AFTER INSERT ON memory BEGIN
    INSERT INTO memory_fts(rowid, content)
    VALUES (NEW.id, dmb_fts_norm(coalesce(NEW.content_enc, NEW.content), 'memory.content', NEW.scope || '|' || coalesce(NEW.name, '')));
END;
DROP TRIGGER skills_ai;
CREATE TRIGGER skills_ai AFTER INSERT ON skills BEGIN
    INSERT INTO skills_fts(rowid, name, description, body)
    VALUES (NEW.id, dmb_fts_norm(NEW.name, 'skills.name', ''), dmb_fts_norm(NEW.description, 'skills.description', ''), dmb_fts_norm(NEW.body, 'skills.body', ''));
END;
DROP TRIGGER skills_au;
CREATE TRIGGER skills_au AFTER UPDATE ON skills BEGIN
    DELETE FROM skills_fts WHERE rowid = OLD.id;
    INSERT INTO skills_fts(rowid, name, description, body)
    VALUES (NEW.id, dmb_fts_norm(NEW.name, 'skills.name', ''), dmb_fts_norm(NEW.description, 'skills.description', ''), dmb_fts_norm(NEW.body, 'skills.body', ''));
END;
