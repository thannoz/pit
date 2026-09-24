CREATE TABLE IF NOT EXISTS orders (
    id    integer PRIMARY KEY,
    item  text    NOT NULL,
    cents integer NOT NULL
);
