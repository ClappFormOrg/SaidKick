-- Local development seed: one public cluster with fake data.
-- cmd/seed has no clusters section, so `make seed` applies this after it.
-- Safe to re-run: the row is inserted only when no cluster has this name.
-- The cluster stays pending_registration: no agent runs against it locally.
INSERT INTO clusters (id, name, description, scope, state)
SELECT gen_random_uuid(), 'dev-public-cluster', 'Fake public cluster for local development', 'public', 'pending_registration'
WHERE NOT EXISTS (SELECT 1 FROM clusters WHERE name = 'dev-public-cluster');

-- Print the seeded IDs: use a user id as the X-Dev-User-ID header value.
-- The review-org auditor can approve dev-org submissions.
SELECT o.slug AS org, u.email, u.id AS dev_user_id, string_agg(r.role, ',' ORDER BY r.role) AS roles
FROM users u
JOIN organizations o ON o.id = u.organization_id AND o.slug IN ('dev-org', 'review-org')
LEFT JOIN role_assignments r ON r.user_id = u.id AND r.revoked_at IS NULL
GROUP BY o.slug, u.email, u.id
ORDER BY o.slug, u.email;

SELECT id AS cluster_id, name, scope, state FROM clusters WHERE name = 'dev-public-cluster';
