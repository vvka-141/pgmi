# Test parity for `shared/` content and template deployments (PGMI-862)

The Go tests whose subject is the content of `shared/` (templates, skills,
session SQL, the `pgmi ai` corpus) or a template deployment die with the Go
code on switch day. This table maps each one to its Rust counterpart, or says
why it is not ported. Go tests of Go code are out of scope here (the separate
test-parity ticket); they are listed for completeness where they sit in the
same files.

## Where the Rust tests live

- **Content linters**: `rust/pgmi/src/content_tests/` (`#[cfg(test)]` unit
  tests in the library). They read `pgmi-shared` through `EmbeddedFs`, or a
  scaffold rendered in memory by `Template::render` into a `MemFs`; no disk,
  except `README.md`, `docs/` and `examples/`, which are not embedded. In-crate
  rather than in `tests/` because they use `deploy::script::split`, the SQL
  lexer, `walk::load` and the `pgmi.yaml` reader, which stay `pub(crate)`.
- **Deployment tests**: `rust/pgmi/tests/*.rs`, against a real PostgreSQL
  named by `PGMI_TEST_CONN`. Without it each test prints `skipped:
  PGMI_TEST_CONN is not set` and returns; with `PGMI_REQUIRE_DB` set a
  missing `PGMI_TEST_CONN` fails instead (as in Go). CI job `Rust` runs them
  against a `postgres:18` service with both set. Each test uses its own
  database (`pgmi_rs_*`), dropped before and after.
- **How they deploy**: they spawn the built binary (`CARGO_BIN_EXE_pgmi`):
  `pgmi init <tmp> --template <t>`, then `pgmi deploy <tmp> --connection ...
  -d <db>` with parameters on stdin (`--params-file -`). The library's
  `deploy::run` is `pub(crate)`, takes a `Context` with an `Approver`, and is
  being reshaped (record 0022 step 11); exposing it would add a large surface
  that tests alone use. The binary is the stable contract, and the Go tests
  went through the full deployer too. Notices are read from stderr lines;
  where Go counted NOTICE messages, the Rust budget counts lines, so the
  multi-line DONE banner (6 lines, 1 message) raises the basic and advanced
  framework budgets by 5 (basic 11, advanced 45).
- Shared helpers: `tests/common/mod.rs` (`pg()`, `Db`, `Client`, `Project`,
  `Deploy`).

## Mapping

Rust names are `file::test` under `rust/pgmi/tests/`, or `content_tests::module::test`
under `rust/pgmi/src/`. A Go test with subtests maps to one Rust test that runs
every case unless noted.

### internal/scaffold: content linters

| Go test | Rust counterpart |
|---|---|
| agent_guidance TestValidateSkill | content_tests::skills::validate_skill_flags_each_spec_violation |
| agent_guidance TestDescriptionOverlap | content_tests::skills::description_overlap_scores_near_copies_high_and_strangers_low |
| agent_guidance TestShippedSkillDescriptionsDoNotOverlap | content_tests::skills::shipped_skill_descriptions_do_not_overlap |
| agent_guidance TestScaffoldWritesAgentGuidance | content_tests::skills::scaffold_writes_agent_guidance_that_passes_the_spec |
| agent_guidance TestBasicScaffoldGetsExactlyTheSharedSkills | content_tests::skills::basic_scaffold_gets_exactly_the_shared_skills |
| agent_guidance TestSharedSkillsNameNoAdvancedOnlyIdentifiers | content_tests::skills::shared_skills_name_no_advanced_only_identifiers |
| agent_guidance TestAgentsMDPointsInsteadOfRestating | content_tests::skills::agents_md_points_instead_of_restating |
| agent_guidance TestScaffoldAIFilesAreTrackedAndEmbedded | content_tests::skills::scaffold_ai_files_are_embedded; the git half is `pgmi-shared`'s embedded_files_equal_tracked_files (embedded == `git ls-files`) |
| skill_naming_rules TestNamingViolations | skill_blocks::naming_violations_flag_undotted_codes_and_unprefixed_writers |
| skill_naming_rules TestSkillBlocksFollowNamingRules | skill_blocks::skill_blocks_follow_the_naming_rules |
| skill_snippet_guard TestSkillSQLSnippetsDoNotTeachSilentFailures | content_tests::snippets::skill_sql_snippets_do_not_teach_silent_failures |
| skill_snippet_guard TestSkillsNameNoRetiredAPI | content_tests::snippets::skills_name_no_retired_api |
| skill_snippet_guard TestSilentFailuresFlagsEachShape | content_tests::snippets::silent_failures_flags_each_shape |
| skill_snippet_guard TestRetiredAPIHitsFlagsEachTerm | content_tests::snippets::retired_api_hits_flags_each_term |
| no_leak TestShippedContentCarriesNoSourceProductVocabulary | content_tests::leak::shipped_content_carries_no_source_product_vocabulary (+ leak_lines_flags_cyrillic_and_ignores_plain_text). Words split on Rust's `is_alphabetic`/`is_numeric` for Go's `IsLetter`/`IsDigit` |
| template_test_null_safety TestTemplateTestsAvoidNullUnsafeComparisons | content_tests::assertions::template_tests_avoid_null_unsafe_comparisons |
| test_assertion_totality TestTemplateAssertionsAreTotal | content_tests::assertions::template_assertions_are_total (templates embedded, `examples/` from disk) |
| test_assertion_totality TestScanAssertionsFlagsEachNullUnsafeShape | content_tests::assertions::scan_assertions_flags_each_null_unsafe_shape |
| template_execution_contract TestShippedTemplatesSplitAtTheirDocumentedCommit | content_tests::templates::shipped_templates_split_at_their_documented_commit (Rust `deploy::script::split`) |
| entity_sweep_wiring TestAdvancedDeploySQLWiresTheEntityStandardsSweep | content_tests::templates::advanced_deploy_sql_wires_the_entity_standards_sweep (+ code_only_masks_comments_and_literals, the masking Go's `RedactForMacros` did) |
| pgmiignore_template TestTemplatesShipPgmiignoreWhereTheyNeedIt | content_tests::templates::advanced_ships_a_pgmiignore_for_clients_and_build_output |
| pgmiignore_template TestTemplatesKeepParameterFilesOutOfSessionAndGit | content_tests::templates::templates_keep_parameter_files_out_of_session_and_git (loads the scaffold with `walk::load` over `MemFs`) |
| pgmiignore_template TestTemplatesKeepBackupCopiesOutOfTheSession | content_tests::templates::templates_keep_backup_copies_out_of_the_session |
| minimal_template TestMinimalTemplate_WritesOnlyItsThreeFiles | content_tests::templates::minimal_template_writes_only_its_three_files |
| minimal_template TestMinimalDeploySQL_MatchesTheREADMELoop | content_tests::templates::minimal_deploy_sql_matches_the_readme_loop |
| minimal_template TestScaffoldedPgmiYAMLSetsNoEnv | content_tests::templates::scaffolded_pgmi_yaml_sets_no_env |
| gateways_template TestAdvancedScaffoldShipsTheGatewayExamples | content_tests::templates::advanced_scaffold_ships_the_gateway_examples |
| gateways_template TestGatewayProblemCodesAreNamespaced | content_tests::templates::gateway_problem_codes_are_namespaced |
| mcp_gateway_accept TestMCPGateway_AcceptQValues | content_tests::gateways::mcp_gateway_accept_honours_q_values |
| mcp_gateway_context TestMCPGateway_ForwardsOnlyTheConnection | content_tests::gateways::mcp_gateway_forwards_only_the_connection |
| mcp_gateway_smoke TestMCPGateway_StatusLineIsHTTP | content_tests::gateways::mcp_gateway_status_line_is_http |
| advanced_trimming TestAdvancedReadmeTrimmingListMatchesTheTest | content_tests::templates::advanced_readme_trimming_list_matches_the_test |
| templates_unit TestTemplateStructureWithoutFilesystem, TestTemplateDeploySQLReading | project::walk tests every_embedded_template_has_checksummed_sql_without_a_disk, every_embedded_template_loads_in_memory (record 0022 step 3) |
| templates_unit TestTemplateFileMetadata | project::walk tests (path, directory and extension are built and tested there) |
| templates_unit TestBasicTemplateTestFilePaths | dropped: pins five paths copied from the template; template_deploy and the basic template tests run those files |
| templates_unit TestEmbedFileSystemPerformance | dropped: asserts nothing beyond "scanning 100 times works" |
| templates_unit TestNoDotDirectoriesEmbedded | templates::tests::no_dot_directory_is_embedded_except_agents |

The Python gateway tests run the embedded gateway sources from memory: a
loader passed to `python3 -c` imports `gateway`, its sibling modules and the
`psycopg`/`psycopg_pool`/`jwt` stand-ins from a JSON map on stdin, so nothing
is written to disk. Without Python they print `skipped` and return, as Go did.

### internal/scaffold: template deployments

| Go test | Rust counterpart |
|---|---|
| integration TestTemplateDeployment (minimal, basic, advanced) | template_deploy::{minimal,basic,advanced}_template_deploys_and_redeploys (test floor, quiet-deploy budget, schema fingerprint, row-count drift, post-conditions) |
| integration TestTallyNotices | template_deploy::tally_counts_framework_lines_and_names_chatty_steps |
| row_counts, schema_fingerprint (helpers) | template_deploy (+ data_drift_spares_only_growing_append_only_tables) |
| skill_blocks TestParseRunnableBlocks | skill_blocks::parse_runnable_blocks_reads_targets_tails_and_requirements |
| skill_blocks TestSkillBlocksDeploy (basic, advanced, guarded) | skill_blocks::skill_blocks_of_the_basic_template_deploy, …_of_the_advanced_template_deploy, …_of_the_advanced_template_pass_every_guard |
| skill_blocks TestSkillBlocksDeploy_FailsOnBrokenBlock | skill_blocks::skill_blocks_gate_fails_on_a_broken_block |
| skill_blocks TestSkillBlocksDeploy_FailsOnUnexecutedBlock | skill_blocks::skill_blocks_gate_fails_on_an_unexecuted_block |
| advanced_environment TestAdvancedTemplate_RefusesBadParameters | advanced_environment::refuses_bad_parameters |
| advanced_environment TestAdvancedTemplate_EnvironmentLabel | advanced_environment::environment_label |
| advanced_environment TestAdvancedTemplate_RefusesARoleNameTooLong | advanced_environment::refuses_a_role_name_too_long |
| advanced_environment TestAdvancedTemplate_RefusesAForeignAdminRole | advanced_environment::refuses_a_foreign_admin_role |
| advanced_environment TestAdvancedTemplate_RefusesAForeignTemplateRole | advanced_environment::refuses_a_foreign_template_role |
| advanced_environment TestAdvancedTemplate_IgnoresAnUnregisteredJobsLogin | advanced_environment::ignores_an_unregistered_jobs_login |
| advanced_environment TestAdvancedTemplate_RedeploysWithAGatewayLoginInTheApiRole | advanced_environment::redeploys_with_a_gateway_login_in_the_api_role |
| advanced_environment TestAdvancedTemplate_DeploysWithQuotedOrCustomRoleNames | advanced_environment::deploys_with_quoted_or_custom_role_names |
| advanced_file_contract TestAdvancedTemplate_RefusesFileOfTheWrongKind | advanced_contract::refuses_file_of_the_wrong_kind |
| advanced_file_contract TestAdvancedTemplate_RefusesRunOnceFileWithSeveralSortKeys | advanced_contract::refuses_run_once_file_with_several_sort_keys |
| advanced_flip TestAdvancedTemplate_IdempotentToNonIdempotentFlip | advanced_contract::idempotent_to_non_idempotent_flip |
| advanced_guard_options TestAdvancedTemplate_GuardOptions | advanced_contract::guard_options_all_on_template_green and guard_option_{strict_fk_index, rls_index_method, plpgsql_check, sql_lint, naming, handler_reads_views, view_column_comments, trigger_allowlist} (+ switch_on_flips_only_the_named_row). `plpgsql_check` skips without the extension or a superuser, as in Go |
| advanced_jobs TestAdvancedTemplate_BackgroundJobs | advanced_sessions::background_jobs |
| advanced_jobs TestAdvancedTemplate_JobsLogin | advanced_sessions::jobs_login |
| advanced_login_concurrency TestAdvancedTemplate_LoginSessionsUnderConcurrency | advanced_sessions::login_sessions_under_concurrency |
| advanced_login_concurrency TestAdvancedTemplate_RacedOrganizationSlugIs422 | advanced_sessions::raced_organization_slug_is_422 |
| advanced_non_superuser TestAdvancedTemplate_DeploysWithoutSuperuser | advanced_sessions::deploys_without_superuser |
| advanced_non_superuser TestAdvancedTemplate_AdminPasswordNeverInRoleError | advanced_sessions::admin_password_never_in_role_error |
| advanced_pg18_floor TestAdvancedTemplate_RefusesServerBelowPostgres18 | advanced_pg18_floor::refuses_server_below_postgres_18, against `PGMI_TEST_CONN_PG17` (CI starts a `postgres:17`; Go started a testcontainer) |
| advanced_pgmiignore TestAdvancedTemplate_PgmiignoreHidingARunOnceFileIsRefused | advanced_contract::pgmiignore_hiding_a_run_once_file_is_refused |
| advanced_policy_drift TestAdvancedTemplate_RefusesHandAlteredPolicy | advanced_contract::refuses_hand_altered_policy |
| transaction_isolation TestTemplateTransactionIsolation | advanced_transactions::transaction_isolation |
| transaction_policy TestTemplateTransactionPolicy | advanced_transactions::transaction_policy |
| advanced_catalog TestAdvancedTemplate_CatalogSync | advanced_catalog::catalog_sync |
| advanced_catalog TestAdvancedTemplate_DeployHistoryViews | advanced_catalog::deploy_history_views |
| advanced_populated_redeploy TestAdvancedTemplate_RedeploysOntoSignedInPeople | advanced_redeploy::redeploys_onto_signed_in_people |
| advanced_pre_v014 TestAdvancedTemplate_RefusesPreV014Database | advanced_redeploy::refuses_pre_v014_database |
| advanced_privileges TestAdvancedTemplate_ClosedByDefault | advanced_privileges::closed_by_default |
| advanced_privileges TestAdvancedTemplate_RefusesExtensionThatCannotMove | advanced_privileges::refuses_extension_that_cannot_move |
| advanced_privileges TestAdvancedTemplate_AccessBaseline | advanced_privileges::access_baseline |
| advanced_provider_grants TestAdvancedTemplate_DeploysBesideProviderGrants | advanced_privileges::deploys_beside_provider_grants |
| advanced_redeploy_locks TestAdvancedTemplate_RedeployDoesNotLockReadTables | advanced_redeploy::redeploy_does_not_lock_read_tables |
| advanced_redeploy_locks TestAdvancedTemplate_RedeployDoesNotBlockWriters | advanced_redeploy::redeploy_does_not_block_writers |
| advanced_run_once_drift TestAdvancedTemplate_RefusesRunOnceDrift | advanced_redeploy::refuses_run_once_drift |
| advanced_tail TestAdvancedTemplate_TailRefusesInvalidIndex | advanced_redeploy::tail_refuses_invalid_index |
| advanced_tail TestAdvancedTemplate_RedeployRepairsViewOptions | advanced_redeploy::redeploy_repairs_view_options |
| advanced_trimming TestAdvancedTemplate_TrimmingList | advanced_redeploy::trimming_list_leaves_a_template_that_deploys_twice |
| advanced_untagged_file TestAdvancedTemplate_RejectsSQLFileWithoutPlanPosition | advanced_redeploy::rejects_sql_file_without_plan_position |
| serialization_retry TestAdvancedTemplate_SerializationFailureRetryContract | serialization_retry::serialization_failure_reaches_the_rest_client |
| serialization_retry TestAdvancedTemplate_MCPSerializationFailureRetryContract | serialization_retry::serialization_failure_reaches_the_mcp_client |
| transaction_units TestDeployInterleavedConcurrentIndexes | transaction_units::interleaved_concurrent_indexes |
| transaction_units TestDeployTailTransactionIsAtomic | transaction_units::tail_transaction_is_atomic |
| transaction_units TestDeployTailReadsSessionViews | transaction_units::tail_reads_session_views |
| basic_run_once TestBasicTemplate_RunOnceAndReRun | basic_template::run_once_file_runs_once_and_a_re_run_file_every_time |
| basic_run_once TestBasicTemplate_RefusesRunOnceDrift | basic_template::basic_refuses_run_once_drift |
| basic_run_once TestBasicTemplate_EnvDecidesTheSeed | basic_template::basic_env_decides_the_seed |
| basic_run_once TestBasicTemplate_RefusesPhysicalFileWithoutRunOnceHeader | basic_template::basic_refuses_physical_file_without_run_once_header |
| basic_run_once TestBasicTemplate_RefusesRunOnceFileWithSeveralSortKeys | basic_template::basic_refuses_run_once_file_with_several_sort_keys |
| basic_run_once TestBasicTemplate_RefusesSQLFileOutsideItsFolders | basic_template::basic_refuses_sql_file_outside_its_folders |
| deploy_error_sqlstate TestBasicTemplate_FailingMigrationKeepsSQLSTATEAndLocation (runtime) | basic_template::failing_migration_keeps_sqlstate_and_detail |
| deploy_error_sqlstate TestBasicTemplate_FailingMigrationKeepsSQLSTATEAndLocation (syntax) | basic_template::failing_migration_syntax_error_resolves_to_its_line, **`#[ignore]`**: the Rust binary does not report the line of a syntax error inside an executed file (Go: `line: 4`). Un-ignore with the fix |
| minimal_template_integration TestSessionPreparationRefusedByTheServerExits13 | basic_template::session_preparation_refused_by_the_server_exits_13 |

### internal/scaffold: Go scaffolder code (out of scope)

`scaffold_test.go` (TestCreateProject_*, TestIsDirectoryEmpty,
TestBuildFileTree*, TestTemplateSummary_EveryTemplateHasOne,
TestScaffoldSkipsBuildArtifacts, TestBlockingEntries_ToleratesGit) tests the Go
scaffolder; its Rust counterparts are in `templates.rs` and `commands/init.rs`
(record 0022 step 4).

### internal/services: session SQL (ported)

| Go test | Rust counterpart |
|---|---|
| contract_columns TestContractViewColumnsMatchLiveSession | session_sql::contract_view_columns_match_the_live_session |
| contract_functions TestContractFunctionSignaturesMatchLiveSession | session_sql::contract_function_signatures_match_the_live_session |
| contract_functions TestEverySessionFunctionIsDeclaredOrExempt | session_sql::every_session_function_is_declared_in_the_contract |
| contract_functions TestContractTestEventTypeMatchesLiveSession | session_sql::contract_test_event_type_matches_the_live_session |
| step_type_completeness TestStepTypeCompleteness | session_sql::a_real_test_plan_yields_exactly_the_declared_step_types, pgmi_test_plan_emits_no_undeclared_step_type |
| plan_view_generic_id TestPlanViewGenericIDMatchesGoFallbackAcrossEncodings | session_sql::plan_view_generic_id_matches_the_metadata_plan_on_utf8 (against `pgmi metadata plan --json`, the Rust fallback id); the LATIN1 half is **`#[ignore]`**: the Rust binary cannot deploy to a LATIN1 database (22P05 on the non-LATIN1 characters of the session SQL), Go can |
| sql_extension_parity TestSQLExtensionParityWithSchema | session_sql::sql_extension_parity_between_the_loader_and_the_session |
| session_variables TestSessionVariableSystem | session_sql::session_variables_carry_parameters_and_defaults |
| deployer_plan_order TestPlanOrder_NestedDirectories | session_sql::plan_order_over_nested_directories_persists_only_the_root_teardown |
| deployer_plan_order TestPlanOrder_MixedSortKeysAndPathFallback | session_sql::plan_view_orders_sort_keys_and_path_fallbacks_in_byte_order |
| deployer_plan_order TestTestPlanOrder_IntraDirectoryIsByteOrdered | session_sql::test_plan_orders_sibling_tests_in_byte_order |
| deployer_plan_order TestTestPlan_GappedDirectoryStaysUnderItsAncestor | session_sql::a_gapped_test_directory_stays_under_its_ancestor |
| deployer_plan_order TestDeploy_SessionPreparationIsSilent | session_sql::session_preparation_emits_no_notices |
| deployer_plan_order TestSourceViewPathOrder_IsByteOrdered | session_sql::source_view_orders_paths_in_byte_order |
| deployer_plan_order TestDeployError_ReportsFileLineAfterMacroExpansion | out of scope (Go error location); black-box errors/attribution |
| deployer_macro_sequence TestMacroSequence_NoFixture, WithFixture, NestedDirectories | session_sql::macro_sequence_without_a_fixture…, …_with_a_fixture…, …_in_nested_directories… |
| deployer_macro_sequence TestMacroSequence_TestFailure, FixtureFailure | session_sql::a_failing_test_fails_the_deploy_with_its_message, a_failing_fixture_fails_the_deploy_with_its_message |
| deployer_direct_test DirectMode_PassingTests, FailingTests, FilterPattern, SavepointRollback, FixtureExecution, CustomCallback | session_sql::passing_tests_let_the_deploy_through, a_failing_test_stops_the_deploy, a_pattern_runs_only_the_matching_tests, test_writes_are_rolled_back, a_fixture_runs_before_the_tests_of_its_directory, a_custom_callback_receives_the_suite_events |
| deployer_unittest_plan UnittestPlanMaterialization, UnittestTablesExist | session_sql::test_plan_returns_tests_in_order, test_plan_function_and_test_source_table_exist_during_deploy |
| deployer_session_foundation Foundation, EmptyProject, OnlyMigrations | session_sql::session_foundation_suite_passes_on_a_multi_level_project (the Go testdata SQL copied to `tests/session_foundation.sql`), an_empty_project_has_no_sources_and_no_tests, a_migrations_only_project_has_its_migrations_and_no_tests |
| deployer_plan_filter TestPlanView_IsSQLFileGuardExcludesNonSQL | session_sql::the_is_sql_file_guard_keeps_an_editor_backup_from_running |
| deployer_test_gate TestTestGate_EmptyPlanFailsTheDeploy | session_sql::an_empty_test_plan_fails_the_deploy |
| deployer_contract_edges DoubleSTestsDirectory, NestedDeploySQLIsLoaded, MetadataWithoutSortKeysStaysInPlan, TestPatternFailsFast | session_sql::a_double_s_tests_directory_runs_and_stays_out_of_the_source_view, a_nested_deploy_sql_is_an_ordinary_project_file, metadata_without_sort_keys_stays_in_the_plan, a_bad_or_unmatched_test_pattern_fails_fast |
| deployer_contract_edges TestDeploy_ReportsWhetherItCreatedTheDatabase | out of scope (Go deployer result); black-box deploy/json-success |
| unused_tests_notice TestUnusedTestsNotice | out of scope (Go deployer notice). **Gap**: the Rust binary never prints the "deploy.sql never calls pgmi_test()" notice |

### internal/services: Go deployer code (out of scope)

The separate test-parity ticket owns these; the black-box case that covers the
behaviour is named where one plainly does.

| Go file | Black-box coverage |
|---|---|
| deployer_compat_integration | flags/compat |
| deployer_execution_integration, deployer_execution_internal | execution/atomic-head, errors/head-syntax, execution/tail-autocommit, cancel/timeout-cancels |
| deployer_execution_unit | env/pgappname (app name) |
| deployer_integration | exit-codes/0-success, 13-sql-failed, 14-no-deploy-sql; params/env-var-reaches-session; execution/open-transaction |
| deployer_least_privilege_integration | none |
| deployer_overwrite_{budget,cancel,lock,restore}_integration | flags/overwrite-force, overwrite-new-database, overwrite-restores; exit-codes/15-concurrent-deploy |
| deployer_retry_integration | none |
| deployer_unit (41 tests over mocks) | exit-codes/10, 11, 12, 15; flags/overwrite-*; params/invalid-key; cloud/token-auth-requires-tls |
| failure_location_integration | errors/attribution, execution/tail-block-fails |
| parameter_leak_integration | errors/connection-string-secrets (partial) |
| server_version_floor_integration, server_version | none |
| session_integration, session_lock_release_integration, session_unit | notices/verbose-elapsed, exit-codes/10, 11, 15 |
| template_database, mocks | Go helpers only |

### Other Go packages over `shared/`

| Go test | Rust counterpart |
|---|---|
| shared TestEmbeddedTreesEqualTrackedFiles | `pgmi-shared` embedded_files_equal_tracked_files |
| shared TestNoPackageManifestUnderShared | templates::tests::scaffold_suffix_is_dropped and `rust/check-package.sh` (a manifest would drop files from the package) |
| ai TestContract_ViewsExistInSQL | content_tests::contract::contract_views_functions_types_and_events_exist_in_the_session_sql (against `commands/contract.json`) |
| ai TestContract_ViewColumnsMatchSchema | content_tests::contract::contract_view_columns_match_the_backing_tables |
| ai TestContract_FunctionDefaultsMatchSQL | content_tests::contract::contract_function_defaults_match_the_sql |
| ai TestContract_LiveViewColumnsMatch | session_sql::contract_view_columns_match_the_live_session |
| ai TestEveryEmittedTestEventIsDeclared | content_tests::contract::every_emitted_test_event_is_declared |
| ai TestEmbeddedPlanViewExamplesFilterSQLFiles | content_tests::contract::embedded_plan_view_examples_filter_sql_files |
| ai TestEmbeddedDocsReferenceOnlyRealSessionObjects | content_tests::contract::embedded_docs_reference_only_real_session_objects |
| ai TestGetOverview, TestGetContract_* | out of scope (Go `pgmi ai` code); black-box commands/ai |
| docs TestExampleMatchesBasicTemplate | content_tests::templates::example_matches_basic_template |
| docs TestOverviewNamesTheScaffoldedSkills | content_tests::templates::overview_names_the_scaffolded_skills |
| docs TestReadmeAdvancedTemplateSize | content_tests::templates::readme_advanced_template_size_follows_the_code (the README states no counts today, so it returns early, as Go skipped) |
| docs TestDocsNameEveryAdvancedExtension | content_tests::templates::docs_name_every_advanced_extension |
| docs, other tests (stale claims, links, ticket refs, exit-code tables) | out of scope: their subject is `docs/`, not `shared/` |
| metadata uniqueness tests | out of scope (Go metadata code) |

## Seeded violations

Each content linter was run against a copy of the tree with one violation
planted; all 34 failed with the planted message, and the clean copy passed
42/42. The skill naming rule and the step-type checks were proved the same way.
The list is in the commit message that adds `content_tests`.
