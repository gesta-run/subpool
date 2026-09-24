CREATE TABLE employees (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (btrim(name) <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO employees(id, name, created_at)
SELECT (array_agg(id ORDER BY created_at, id))[1], employee_name, min(created_at)
FROM api_keys
GROUP BY employee_name;

ALTER TABLE api_keys ADD COLUMN employee_id uuid;
UPDATE api_keys k
SET employee_id = e.id
FROM employees e
WHERE e.name = k.employee_name;
ALTER TABLE api_keys ALTER COLUMN employee_id SET NOT NULL;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_employee_id_fkey
    FOREIGN KEY (employee_id) REFERENCES employees(id) ON DELETE RESTRICT;
CREATE INDEX api_keys_employee_id_idx ON api_keys(employee_id);
ALTER TABLE api_keys DROP COLUMN employee_name;
