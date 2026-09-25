-- Loading a scenario again starts over: what was entered by hand goes.
TRUNCATE orders;

INSERT INTO orders (id, item, cents) VALUES
    (1001, 'Sencha, 100 g',      1290),
    (1002, 'Teapot, cast iron',  4950),
    (1003, 'Matcha whisk',        899);
