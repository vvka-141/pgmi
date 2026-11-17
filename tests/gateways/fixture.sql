-- Run as the deployer against an env=dev deploy of the advanced template,
-- before the reference gateways' tests: psql -v api_role=<db>_api -f fixture.sql
\set ON_ERROR_STOP on

DROP ROLE IF EXISTS clients_gateway;
CREATE ROLE clients_gateway LOGIN PASSWORD 'gateway' IN ROLE :"api_role";

