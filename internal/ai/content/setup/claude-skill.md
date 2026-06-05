---
name: pgmi
description: Use when working in a pgmi project — a directory containing deploy.sql and/or pgmi.yaml. pgmi is a PostgreSQL-native deployment driver that loads SQL files into pg_temp views and runs deploy.sql; deployment behavior lives in SQL, not CLI flags. Covers the execution model, basic-vs-advanced templates, transactional testing via CALL pgmi_test(), and safety rules for --overwrite/--force and secrets. Do not apply in repositories without deploy.sql or pgmi.yaml.
---

# pgmi

{{CORE}}
