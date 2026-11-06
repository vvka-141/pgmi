SELECT fn_create_project('Website');
SELECT fn_add_task((SELECT project_id FROM project WHERE name = 'Website'), title)
FROM (VALUES ('Draft the copy'), ('Pick the fonts')) AS t(title);
