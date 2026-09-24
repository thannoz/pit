INSERT INTO orders (id, item, cents)
SELECT 2000 + n, 'Gift card ' || n, 2500 * n
FROM generate_series(1, 40) AS n
ON CONFLICT (id) DO NOTHING;
