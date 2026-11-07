CREATE TABLE settings (
    id            smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    search_engine text        NOT NULL DEFAULT 'g',
    city          text        NOT NULL DEFAULT 'Санкт-Петербург' CHECK (char_length(city) BETWEEN 1 AND 60),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

INSERT INTO settings (id) VALUES (1);

CREATE TABLE bookmarks (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title      text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 40),
    url        text        NOT NULL CHECK (char_length(url) BETWEEN 1 AND 2048),
    position   integer     NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX bookmarks_position_idx ON bookmarks (position, id);

INSERT INTO bookmarks (title, url, position) VALUES
    ('GitHub',   'https://github.com',       0),
    ('Claude',   'https://claude.ai',        1),
    ('YouTube',  'https://www.youtube.com',  2),
    ('Gmail',    'https://mail.google.com',  3),
    ('Telegram', 'https://web.telegram.org', 4),
    ('Карты',    'https://yandex.ru/maps',   5);
