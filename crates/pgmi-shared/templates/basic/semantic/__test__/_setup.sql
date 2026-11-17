-- Fixture for every test in this folder: project Website with two open tasks.
-- Each test starts from it; its changes roll back after the test.

SELECT fn_create_project('Website');
SELECT fn_add_task((SELECT project_id FROM project WHERE name = 'Website'), title)
FROM (VALUES ('Draft the copy'), ('Pick the fonts')) AS t(title);
