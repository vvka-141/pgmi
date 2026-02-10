-- ============================================================================
-- pgmi Unit Test Framework (Deprecated)
-- ============================================================================
-- This file is a no-op. All unit test functionality is now in schema.sql:
--   - pgmi_test_directory: hierarchical test directory structure
--   - pgmi_test_source: test file content with fixture detection
--   - pgmi_has_tests(): recursive test discovery
--   - pgmi_test_plan(): depth-first execution plan generation
--
-- The preprocessor calls pgmi_test_plan() directly to get the execution plan.
-- ============================================================================

-- No-op placeholder for backward compatibility
SELECT 1;
