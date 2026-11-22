-- +goose Up

-- History starts at this migration. Historical IDs remain usable if the
-- corresponding catalog or user rows are later removed.
CREATE TABLE activity_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id BIGINT NOT NULL CHECK (user_id > 0),
    reel_id BIGINT NOT NULL CHECK (reel_id > 0),
    event_type TEXT NOT NULL CHECK (event_type IN (
        'like_added', 'like_removed', 'save_added', 'save_removed',
        'view_counted', 'share_created', 'watch_interest'
    )),
    event_time TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX activity_events_recent_idx ON activity_events (user_id, event_time DESC, id DESC);

CREATE TABLE cache_outbox (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind TEXT NOT NULL,
    user_id BIGINT CHECK (user_id > 0),
    reel_id BIGINT CHECK (reel_id > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CHECK (
        (kind IN ('activity', 'state') AND user_id IS NOT NULL AND reel_id IS NOT NULL)
        OR (kind = 'identity' AND user_id IS NOT NULL AND reel_id IS NULL)
        OR (kind = 'catalog' AND user_id IS NULL AND reel_id IS NULL)
    )
);

-- +goose StatementBegin
CREATE FUNCTION ino_record_activity(activity_user BIGINT, activity_reel BIGINT, activity_type TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO activity_events (user_id, reel_id, event_type)
    VALUES (activity_user, activity_reel, activity_type);

    INSERT INTO cache_outbox (kind, user_id, reel_id)
    VALUES ('activity', activity_user, activity_reel);
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION ino_record_interaction_change()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.kind = 'view' THEN
            PERFORM ino_record_activity(NEW.user_id, NEW.reel_id, 'view_counted');
        ELSE
            PERFORM ino_record_activity(NEW.user_id, NEW.reel_id, NEW.kind || '_added');
            INSERT INTO cache_outbox (kind, user_id, reel_id)
            VALUES ('state', NEW.user_id, NEW.reel_id);
        END IF;
    ELSIF OLD.kind IN ('like', 'save') THEN
        PERFORM ino_record_activity(OLD.user_id, OLD.reel_id, OLD.kind || '_removed');
        INSERT INTO cache_outbox (kind, user_id, reel_id)
        VALUES ('state', OLD.user_id, OLD.reel_id);
    END IF;

    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER user_interactions_record_change
    AFTER INSERT OR DELETE ON user_interactions
    FOR EACH ROW EXECUTE FUNCTION ino_record_interaction_change();

-- +goose StatementBegin
CREATE FUNCTION ino_record_share_created()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    PERFORM ino_record_activity(NEW.sender_id, NEW.reel_id, 'share_created');
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER reel_shares_record_creation
    AFTER INSERT ON reel_shares
    FOR EACH ROW EXECUTE FUNCTION ino_record_share_created();

ALTER TABLE user_reel_watch_sessions ADD COLUMN watch_interest_recorded BOOLEAN NOT NULL DEFAULT FALSE;

-- Remember sessions that already qualified before tracking started without
-- inserting historical activity. Use NUMERIC to avoid BIGINT overflow.
UPDATE user_reel_watch_sessions AS session
SET watch_interest_recorded = TRUE
FROM reels AS reel
WHERE reel.id = session.reel_id
    AND session.watched_ms::NUMERIC * 10 >= reel.duration_ms::NUMERIC * 7;

-- +goose StatementBegin
CREATE FUNCTION ino_record_watch_interest()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    qualifying_watch_time NUMERIC;
BEGIN
    IF NEW.watch_interest_recorded THEN
        RETURN NULL;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF NEW.watched_ms <= OLD.watched_ms THEN
            RETURN NULL;
        END IF;
    END IF;

    SELECT duration_ms::NUMERIC * 7 INTO qualifying_watch_time
    FROM reels WHERE id = NEW.reel_id;

    IF NEW.watched_ms::NUMERIC * 10 >= qualifying_watch_time THEN
        IF TG_OP = 'UPDATE' THEN
            -- A duration edit can move the threshold below both reports.
            -- That did not cross it and must not manufacture an event.
            IF OLD.watched_ms::NUMERIC * 10 >= qualifying_watch_time THEN
                RETURN NULL;
            END IF;
        END IF;

        -- This changes only the latch, so it does not re-enter this trigger.
        UPDATE user_reel_watch_sessions
        SET watch_interest_recorded = TRUE
        WHERE user_id = NEW.user_id AND playback_id = NEW.playback_id
            AND NOT watch_interest_recorded;

        IF FOUND THEN
            PERFORM ino_record_activity(NEW.user_id, NEW.reel_id, 'watch_interest');
        END IF;
    END IF;

    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- AFTER runs only on the branch the upsert actually took. PostgreSQL runs a
-- BEFORE INSERT trigger even if ON CONFLICT later updates or rejects the row.
CREATE TRIGGER user_reel_watch_sessions_record_interest
    AFTER INSERT OR UPDATE OF watched_ms ON user_reel_watch_sessions
    FOR EACH ROW EXECUTE FUNCTION ino_record_watch_interest();

-- +goose StatementBegin
CREATE FUNCTION ino_invalidate_catalog()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO cache_outbox (kind) VALUES ('catalog');
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER reels_catalog_rows_changed
    AFTER INSERT OR DELETE OR TRUNCATE ON reels
    FOR EACH STATEMENT EXECUTE FUNCTION ino_invalidate_catalog();

CREATE TRIGGER reels_catalog_visibility_changed
    AFTER UPDATE OF status, id ON reels
    FOR EACH ROW
    WHEN (OLD.status IS DISTINCT FROM NEW.status OR OLD.id IS DISTINCT FROM NEW.id)
    EXECUTE FUNCTION ino_invalidate_catalog();

CREATE TRIGGER products_catalog_changed
    AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON products
    FOR EACH STATEMENT EXECUTE FUNCTION ino_invalidate_catalog();

CREATE TRIGGER reel_products_catalog_changed
    AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON reel_products
    FOR EACH STATEMENT EXECUTE FUNCTION ino_invalidate_catalog();

-- +goose StatementBegin
CREATE FUNCTION ino_invalidate_identity()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.id = NEW.id THEN
        RETURN NULL;
    END IF;

    IF TG_OP IN ('DELETE', 'UPDATE') THEN
        INSERT INTO cache_outbox (kind, user_id) VALUES ('identity', OLD.id);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        INSERT INTO cache_outbox (kind, user_id) VALUES ('identity', NEW.id);
    END IF;

    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER users_cached_identity_changed
    AFTER INSERT OR DELETE OR UPDATE OF id ON users
    FOR EACH ROW EXECUTE FUNCTION ino_invalidate_identity();

-- +goose Down
DROP TRIGGER users_cached_identity_changed ON users;
DROP TRIGGER reel_products_catalog_changed ON reel_products;
DROP TRIGGER products_catalog_changed ON products;
DROP TRIGGER reels_catalog_visibility_changed ON reels;
DROP TRIGGER reels_catalog_rows_changed ON reels;
DROP TRIGGER user_reel_watch_sessions_record_interest ON user_reel_watch_sessions;
DROP TRIGGER reel_shares_record_creation ON reel_shares;
DROP TRIGGER user_interactions_record_change ON user_interactions;

DROP FUNCTION ino_invalidate_identity();
DROP FUNCTION ino_invalidate_catalog();
DROP FUNCTION ino_record_watch_interest();
DROP FUNCTION ino_record_share_created();
DROP FUNCTION ino_record_interaction_change();
DROP FUNCTION ino_record_activity(BIGINT, BIGINT, TEXT);

ALTER TABLE user_reel_watch_sessions DROP COLUMN watch_interest_recorded;
DROP TABLE cache_outbox;
DROP TABLE activity_events;
