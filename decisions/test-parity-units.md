# Test parity for Go unit tests of Go code (PGMI-863)

Go unit tests whose subject exists in Rust need a Rust test that pins the same
behaviour (engineering parity, owner decision 2026-11-11). This file maps the
groups the PGMI-857 audit (section 1.2) left without a counterpart: parameters
(2), script split and macro (3), metadata (4), deploy orchestration (10),
config (12) and the TUI wizards. The other groups were ported by the PGMI-857
steps and PGMI-866..870; content and template tests are in
`test-parity-shared.md`.

Rust names are `module::tests::test` under `rust/pgmi/src/`. A black-box case
is `blackbox/cases/<case>.yaml`; it passes on the Go binary and is on the Rust
passing list. "Dropped" names the reason.

## Group 2: parameters

`config::params::collect` merges and checks every source; Go split this
between `files/loader` and `cli`.

| Go test | Rust counterpart |
|---|---|
| loader TestValidateParameterKey | config::params::tests::keys_that_are_not_postgres_identifiers_are_invalid_config, identifier_keys_are_accepted_and_folded |
| loader TestValidateParameterKey_InvalidKeys | config::params::tests::keys_that_are_not_postgres_identifiers_are_invalid_config |
| loader TestValidateParameterKey_ValidKeys | config::params::tests::identifier_keys_are_accepted_and_folded |
| loader TestValidateParameterKey_Comprehensive | both of the above (every case) |
| loader TestValidateParameterKey_AllValidVariants | config::params::tests::identifier_keys_are_accepted_and_folded |
| loader TestValidateParameterValue_ErrorOmitsValue | config::params::tests::a_bad_value_error_names_the_key_and_not_the_value (NUL; invalid UTF-8 from a params file), a_non_utf8_environment_parameter_is_invalid_config |
| loader TestValidateParameters_CaseOnlyCollisionIsInvalidConfig | config::params::tests::case_twins_within_one_source_are_invalid_config |
| loader TestLoadParametersIntoSession_InvalidKey, _MultipleInvalidKeys | config::params::tests::keys_that_are_not_postgres_identifiers_are_invalid_config; black-box params/invalid-key (exit 10 before connecting) |
| cli TestLoadMergedParameters_EnvCaseVariantsAreInvalidConfig | config::params::tests::environment_case_twins_are_invalid_config; black-box params/case-twins |
| cli TestLoadMergedParameters_CaseVariantsInYamlAreInvalidConfig | config::params::tests::case_twins_within_one_source_are_invalid_config; black-box params/case-twins |
| cli TestLoadMergedParameters_CaseInsensitivePrecedence | config::params::tests::case_twins_across_sources_are_one_parameter_and_the_later_source_wins; black-box params/case-twins |
| cli TestLoadParamsFromFiles_CaseVariantsInOneFileAreInvalidConfig | config::params::tests::case_twins_within_one_source_are_invalid_config |
| cli TestLoadParamsFromFiles_CaseVariantsAcrossFilesLaterWins | config::params::tests::case_twins_across_params_files_the_later_file_wins (temp files: params files have no filesystem seam) |
| cli TestParamPairsThatDifferOnlyInCaseAreRefused, TestServeDeployCaseVariantParamsAreInvalidConfig | config::params::tests::case_twins_within_one_source_are_invalid_config (serve passes its params as flags) |

## Group 3: script split and macro

Rust splits `deploy.sql` into units first and expands macros per unit; Go
expanded first and split after. A unit carries its byte offset (`Unit::start`)
instead of Go's padding prefix.

| Go test | Rust counterpart |
|---|---|
| split TestSplitExecutionUnits | deploy::script::tests::head_and_tail, begin_atomic_is_stepped_over |
| split TestSplitExecutionUnits_CRLF | deploy::script::tests::crlf_splits_like_lf |
| split TestSplitExecutionUnitsPreservesPositions | deploy::script::tests::every_unit_knows_where_it_starts_in_the_file; deploy::blame::tests::a_position_in_deploy_sql_resolves_in_head_and_tail |
| split TestExpandedMacroDoesNotMoveTheHeadAnchor | deploy::script::tests::a_macro_in_the_head_expands_inside_the_head |
| split TestExpandedMacroInTailProducesBrokenSavepoints | deploy::script::tests::a_macro_in_the_tail_is_one_unit_and_its_savepoints_stay_together; the 25P01 is black-box execution/pgmi-test-needs-begin |
| split TestSplitExecutionUnitsTemplateShape | deploy::script::tests::the_template_shape_is_a_head_and_one_banner_unit |
| split TestHeadCommits | deploy::script::tests::head_commits_only_on_commit_or_end; black-box execution/commit-and-chain |
| split_scale TestSplitExecutionUnitsTailHeavyStaysLinear | deploy::script::tests::a_tail_heavy_script_splits_in_linear_space |
| split_scale TestPadPrefixResolvesLikeFullMask | dropped: Rust sends no padding prefix; positions resolve through `Unit::start` (blame tests above) |
| linemap TestLineMap_MapsExpandedLinesBackToTheFile, _TwoMacrosAccumulateTheirShift | deploy::script::tests::line_map_maps_expanded_lines_back_to_the_file |
| callback_name TestValidateCallbackName | deploy::script::tests::callback_names_are_bare_or_schema_qualified_identifiers |
| pipeline TestPipeline_Process_NoMacros, _MacroInComment | deploy::script::tests::only_a_complete_call_outside_literals_is_a_macro |
| pipeline TestPipeline_Process_SingleMacro, _MacroWithPattern, _MultipleMacros, _MacroFollowedByCommentWithSameText, _MacroWithPgTempPrefix | deploy::script::tests::macro_forms |
| pipeline TestPipeline_Process_EmptyGenerateResult, _EmptyInput | deploy::script::tests::an_empty_expansion_leaves_the_rest_in_place |
| pipeline TestPipeline_Process_InvalidCallback | deploy::script::tests::callback_names_are_bare_or_schema_qualified_identifiers; black-box dialect/pgmi-test-macro-arguments |
| pipeline TestPipeline_Process_GenerateError | dropped: `pgmi_test_generate()` is a query in `execute::run`, and the database has no test double (record 0022); its error is `sql_error`, exit 13 as in Go |
| macros TestMacroDetector_IgnoresLiteralsAndComments | deploy::script::tests::only_a_complete_call_outside_literals_is_a_macro; invariants::tests::macros_only_outside_literals_and_comments |
| macros TestMacroDetector_DetectsTopLevelDespiteAdjacentLiterals | deploy::script::tests::only_a_complete_call_outside_literals_is_a_macro |
| macros TestMacroDetector_Detect_BasicCalls, _SchemaQualified, _WhitespaceVariations, _WithCallback, _PatternVariations | deploy::script::tests::macro_forms, an_empty_pattern_or_callback_means_the_default; black-box dialect/pgmi-test-macro-whitespace |
| macros TestMacroDetector_Detect_InContext | deploy::script::tests::macro_forms; the "in DO block" case is dropped: Go ran the detector there without its literal mask, and the contract never expands inside dollar quotes |
| macros TestMacroDetector_Detect_MultipleMacros, _OrderPreserved, _Positions, _PositionsWithPattern | deploy::script::tests::macro_forms, only_a_complete_call_outside_literals_is_a_macro |
| macros TestMacroDetector_Detect_NoMacros | deploy::script::tests::only_a_complete_call_outside_literals_is_a_macro |
| macros TestMacroDetector_Detect_RealWorldExamples | content_tests::templates (every template's deploy.sql) and the template deployments in `tests/` |
| macros TestMacroDetector_MultiByteWordBoundary | deploy::script::tests::a_non_ascii_character_glued_to_call_is_part_of_the_word (differs from Go, see below) |
| comments TestRedactForMacros_* (8) | project::lexer::tests::only_real_comments_are_comments, literals_and_comments, positional_parameter_is_not_a_dollar_quote; invariants::tests::sql_bytes_tile (byte length kept) |
| contract_macro_forms TestContractMacroFormsAreDetected, TestSchemaQualifiedMacroFormIsDetected | deploy::script::tests::every_macro_form_the_contract_publishes_is_detected |

## Group 4: metadata

`project::metadata::parse` extracts and validates in one step and returns a
`MetaError` variant per refusal.

| Go test | Rust counterpart |
|---|---|
| TestExtract_ValidMetadata_AllFields, _MinimalFields, TestExtractAndValidate_Integration | project::metadata::tests::every_field_and_the_minimal_form_are_read |
| TestExtract_NoBlockComment, _NoMetadataInComment, _EmptyMetadataBlock, _CaseSensitivity | project::metadata::tests::a_file_without_a_block_has_no_metadata |
| TestExtract_UnterminatedComment | project::metadata::tests::a_file_without_a_block_has_no_metadata, an_unterminated_comment_holding_a_block_is_an_xml_error |
| TestExtract_MultipleMetadataBlocks, TestExtractStillRejectsTwoRealBlocks | project::metadata::tests::two_blocks_in_separate_comments_are_refused, two_elements_in_one_comment_are_refused |
| TestExtract_MetadataTooLarge | project::metadata::tests::a_block_over_10_kib_is_refused |
| TestExtract_InvalidXMLSyntax | project::metadata::tests::malformed_xml_is_an_xml_error |
| TestExtract_InvalidUUIDFormat, TestExtract_AttributeErrorsNameTheAttribute | project::metadata::tests::attribute_errors_name_the_attribute_and_the_reference |
| TestValidationErrorPointsAtReachableReference | project::metadata::tests::attribute_errors_name_the_attribute_and_the_reference (the skill is scaffolded) |
| TestExtract_MetadataNotInFirstComment | project::metadata::tests::a_later_comment_can_hold_the_block |
| TestExtract_BooleanParsing | project::metadata::tests::idempotent_takes_the_boolean_spellings, reads_block |
| TestExtractIsNotFooledByLiteralsOrNesting | project::metadata::tests::literals_and_nested_comments_do_not_fool_the_reader |
| TestExtractAndValidate_ValidationFailure, TestValidate_AllValid, _MinimalValid, _NilUUID, _MissingIdempotent, _EmptySortKey, _WhitespaceOnlySortKey, _WhitespaceOnlyDescription, _MultipleErrors | project::metadata::tests::validation_problems_are_all_listed_with_their_hints, every_field_and_the_minimal_form_are_read |
| TestValidationResult_AddError, _ErrorString, _AddError_MarksInvalid | dropped: Go's accumulator type; Rust collects the problems in `MetaError::Invalid`, whose numbered rendering validation_problems_are_all_listed_with_their_hints pins |
| TestCheckUniqueIDs | project::metadata::tests::unique_ids_and_files_without_metadata_pass, duplicates |
| TestGenerateFallbackID_* (8) | project::metadata::tests::fallback_id_is_the_md5_of_the_path_as_a_uuid (MD5 vectors, case, `./` prefix, empty path, distinct ids) |

## Group 10: deploy orchestration

Go unit-tested `DeploymentService` against mocks of the connector, the
database manager and the session preparer. Rust has no database test double
(record 0022, "Database: none"): the pure parts are unit tests, the flows are
black-box cases and `tests/`.

| Go test | Rust counterpart |
|---|---|
| server_version TestVerifyServerVersion | deploy::tests::a_server_below_postgres_15_is_refused_as_invalid_config |
| TestDeploy_AnUnsupportedServerIsRefusedBeforeAnyCreateOrDrop | same; `check_server` runs on the maintenance session before the overwrite claim and before `CREATE DATABASE`, and on the target session |
| TestDeploy_LostCreateRaceIsAConcurrentDeploy | deploy::tests::a_lost_create_race_is_a_concurrent_deploy |
| TestDeploy_CreateFailureOtherThanCollisionIsNotConcurrent | deploy::tests::a_lost_create_race_is_a_concurrent_deploy (42501 keeps exit 13 and gets the -d / CREATEDB hint) |
| TestValidateOverwriteTarget, TestDeploy_OverwriteBlocksMaintenanceDB, _OverwriteBlocksCustomMaintenanceDB, _OverwriteAllowsDifferentDB | deploy::overwrite::tests::the_maintenance_database_is_never_the_overwrite_target (case differs from Go, see below) |
| TestDeploy_OverwriteBlocksTemplateDatabases | `overwrite::claim` reads `pg_database.datistemplate` (any template, not only template0/1); no DB double |
| TestDeploy_OverwritePreservesEncodingAndLocale, _OverwriteOfADefaultDatabaseStaysOnTemplate1, _CreatingAMissingDatabaseUsesServerDefaults | deploy::overwrite::tests::create_keeps_locale_owner_and_limit (`create_sql` with and without a snapshot); black-box flags/overwrite-restores, deploy/latin1-database |
| TestDeploy_OverwriteRefusesWhatItCouldNotRestore | black-box flags/overwrite-restores |
| TestDeploy_OverwriteDBNotExists_Creates | black-box flags/overwrite-new-database |
| TestDeploy_OverwriteApproved_FullCycle | black-box flags/overwrite-force, flags/overwrite-restores |
| TestDeploy_OverwriteDenied | deploy::approve::tests::the_typed_name_must_match; black-box exit-codes/12-overwrite-denied |
| TestDeploy_OverwriteCountdownCancelled | deploy::approve::tests::cancelling_the_prompt_is_an_interrupt; black-box terminal/prompt-cancel |
| TestDeploy_OverwriteRefusesWhileADeployRuns | black-box flags/overwrite-deploy-during-approval |
| TestDeploy_OverwriteRefusesWhileAnotherOverwriteRuns | tests/overwrite.rs a_held_overwrite_lock_refuses_a_second_overwrite_with_exit_15; black-box flags/overwrite-concurrent |
| TestDeploy_OverwriteDropFails, _OverwriteCreateFails, _EnsureDBCheckFails, _EnsureDBCreateFails, _PrepareSessionFails, _MgmtConnectorFails_Overwrite, _MgmtConnectorFails_Ensure | dropped: each failure is a `?` on one server call with its `sql_error` / connection error; no DB double. Exit classes: black-box exit-codes/11-connection-failed, 13-sql-failed |
| TestDeploy_ManagerErrorsAreNotWrappedTwice | dropped: Rust wraps a server error once, in `sql_error` |
| TestDeploy_EnsureDBExists, _EnsureDBCreates, _MaintenanceDBDefault, _CustomMaintenanceDB, _OverwriteCustomMaintenanceDB | black-box flags/overwrite-new-database, connection/keywords; conn::resolve_tests (maintenance database resolution) |
| TestDeploy_MissingMaintenanceDatabase | black-box errors/connection-failures (exit 11, names the database) |
| TestDeploy_InvalidConfig, TestNewDeploymentService_NilDeps | dropped: Go library-API guards; the CLI refuses missing arguments (black-box exit-codes/2-usage, exit-codes/10-invalid-config) and Rust has no nil dependencies |
| TestDeploy_InvalidConnectionString | conn::parse tests; black-box connection/string-forms |
| TestDeploy_InvalidParameterKey_FailsBeforeConnecting | config::params tests (group 2); black-box params/invalid-key |
| TestDeploy_MissingDeploySQL_FailsBeforeConnecting | project::walk tests; black-box exit-codes/14-no-deploy-sql |
| TestValidateAndParseConfig_CarriesCloudAuthSettings, _TokenAuthNeedsTLS | conn::resolve_tests token_auth_refuses_an_sslmode_that_allows_plaintext, token_auth_accepts_a_tls_only_sslmode, token_auth_defaults_to_a_verifying_sslmode; black-box cloud/token-auth-requires-tls |
| deployer_test_gate, deployer_macro_sequence, deployer_plan_order | integration tests, mapped in `test-parity-shared.md` (session_sql) |
| failure_location TestFailureLocation_TestFileParseErrorReportsItsLine, _AParseErrorInAFunctionBodyHasItsFileLine | deploy::blame::tests::a_syntax_error_in_a_file_resolves_to_its_line_and_column, a_routine_body_error_is_placed_inside_the_file; black-box errors/line-and-column |
| failure_location _TestFileRuntimeErrorNamesTheFileWithoutALine | deploy::blame::tests::a_runtime_error_without_a_line_names_the_file_only |
| failure_location _TailStatementReportsItsDeploySQLLine, _AnErrorInDeploySQLItselfIsPlaced, _TailDOBlockReportsTheFailingLine | deploy::blame::tests::a_position_in_deploy_sql_resolves_in_head_and_tail, a_runtime_error_in_deploy_sql_finds_its_do_block; black-box errors/line-and-column, errors/head-syntax |
| failure_location _ACommentHeaderAboveATestKeepsTheLine, _AFailingTestIsMarkedAndLined | deploy::blame::tests::a_runtime_error_in_a_files_do_block_names_its_line, test_paths_are_the_ones_under_a_test_directory; black-box errors/attribution |
| failure_location _TestPathWithTheDollarTagRuns | session SQL (`pgmi_test_generate`), test-parity-shared.md |
| failure_location _UnitsCommittedCountsOnlyCommittedUnits | deploy::tests::a_failed_summary_says_what_was_committed; tests/transaction_units.rs |
| failure_location _CommitAndChainCountsTheHead, _SingleUnitCommitAndChainSucceeds | deploy::script::tests::head_commits_only_on_commit_or_end; black-box execution/commit-and-chain |
| failure_location _StoppedDeployNamesTheRunningFile | black-box cancel/timeout-cancels, exit-codes/130-interrupted |

## Group 12: config

| Go test | Rust counterpart |
|---|---|
| TestLoad_AllFields, TestLoad_MinimalYAML | config::yaml::tests::every_field_is_read |
| TestLoad_RejectsUnknownField | config::yaml::tests::unknown_key_refused |
| TestLoad_FileNotFound, TestLoad_EmptyFile, TestLoad_InvalidYAML, TestLoad_RejectsUnknownAuthMethod | config::yaml::tests::load_reads_pgmi_yaml_from_the_project |

## TUI wizards

The Rust wizards are state machines: `update(KeyEvent)` returns an `Effect`
(`Test(settings)` asks the caller to test the connection, `Done(..)` ends the
wizard) and `tested(result)` feeds the answer back. The init wizard returns
`connect: true` and `commands::init` runs the connection wizard after it on
its own screen, so Go's embedded-wizard tests map to the two wizards apart.

| Go test | Rust counterpart |
|---|---|
| TestConnectionWizard_InitialState, _NavigateProviders | tui::connection::tests::the_wizard_starts_on_the_first_provider |
| TestConnectionWizard_ProviderBounds | tui::connection::tests::the_provider_list_wraps_at_both_ends (differs from Go, see below) |
| TestConnectionWizard_SelectLocalProvider, _HostFormDefaults | tui::connection::tests::local_opens_the_host_form_with_its_defaults |
| TestConnectionWizard_EnterAdvancesFields, _BuildConfigDefaults, _TestSuccessThenQuit, _FullHappyPath, _MockTesterCalledOnSubmit, _EndToEndWithMockTester | tui::connection::tests::enter_walks_the_fields_then_submits_the_defaults, local_form_as_the_case_types_it |
| TestConnectionWizard_ValidationErrorShown, _EmptyDatabaseFocusesIt | tui::connection::tests::a_missing_database_is_named_under_its_field_and_typing_clears_it, database_is_required |
| TestConnectionWizard_TestFailureGoesBackToEdit, _MockTesterFailureFlow, _EscAfterFailureKeepsValues | tui::connection::tests::esc_after_a_failed_test_returns_to_the_form_with_the_values_kept |
| TestConnectionWizard_RetryAfterFailure, _EnterAfterFailureRetries | tui::connection::tests::enter_after_a_failed_test_tests_the_same_settings_again |
| TestConnectionWizard_EscCancels, _EscCancelsWithoutInterrupt, _CtrlC_Cancels, _BackFromAuthToProvider | tui::connection::tests::esc_walks_back_and_ctrl_c_interrupts |
| TestConnectionWizard_AzureEntraIDFlow, _AzureValidation_MissingDatabase | tui::connection::tests::azure_entra_id_takes_three_fields_and_needs_a_database |
| TestConnectionWizard_AWSIAMFlow, _AWSIAMFlow_ValidationMissingHost | tui::connection::tests::aws_iam_takes_five_fields_and_needs_a_host |
| TestConnectionWizard_GoogleIAMFlow, _GoogleIAMFlow_ValidationMissingInstance | tui::connection::tests::google_iam_settings, google_iam_needs_an_instance |
| TestConnectionWizard_ConnStringFlow, _ValidationMissing, _InvalidStringShowsError | tui::connection::tests::a_connection_string_is_required_and_must_parse |
| TestConnectionWizard_ConnStringFlow_ParsesRealValues | tui::connection::tests::connection_string_is_parsed |
| TestConnectionWizard_ConnStringErrorRedactsThePassword | tui::connection::tests::an_unparseable_connection_string_never_shows_its_password |
| TestConnectionWizard_InvalidPortDefaultsTo5432 | tui::connection::tests::an_unusable_port_falls_back_to_5432 |
| TestConnectionWizard_TabNavigation, _TabAtBoundary | tui::connection::tests::tab_and_backtab_move_focus_within_the_form |
| TestConnectionWizard_FooterListsOnlyWorkingKeys | tui::connection::tests::q_is_not_a_key_and_the_footer_does_not_offer_it |
| TestConnectionWizard_View_ProviderStep, _View_InputFormStep | tui::connection::tests::every_screen_shows_its_title_and_choices |
| TestConnectionWizard_View_TestConnectionStep, _SuccessNamesMaintenanceDatabase | tui::connection::tests::the_success_screen_names_the_maintenance_database, esc_after_a_failed_test_returns_to_the_form_with_the_values_kept |
| TestConnectionWizard_HostFormFits80x24, _EveryFormFits80x24 | tui::connection::tests::the_focused_field_and_the_error_fit_80_by_24 (rendered into an 80x24 buffer) |
| TestConnectionWizard_WrapsAt60Columns | dropped: the view is a ratatui `Paragraph` with `Wrap`, which cannot draw past the area it is given |
| TestConnectionWizard_AuthFailureHidesTLSNoise | conn::dial::tests::a_connect_error_keeps_the_sqlstate_every_host_agreed_on (the wizard shows `Error::text()` of the dial) |
| TestConnectionWizard_WithInitialConfigPrefillsHostForm | tui::connection::tests::an_existing_config_prefills_the_host_form |
| TestConnectionWizard_KeepsTheExistingTLSSettings | tui::connection::tests::existing_tls_is_kept_but_a_token_never_gets_a_plaintext_capable_mode, existing_settings_seed_the_form_and_keep_tls |
| TestInitWizard_NoConnection_QuitsAtSetupChoice | tui::init::tests::down_enter_enter_picks_basic_without_connection |
| TestInitWizard_ConnectionEmbedded_SingleProgram | tui::init::tests::yes_runs_the_connection_wizard_and_finishes_with_its_settings; black-box terminal/init-wizard-connection |
| TestInitWizard_CtrlCInterrupts, _CtrlCInConnectionWizardInterrupts | tui::init::tests::ctrl_c_stops_init_on_every_screen; tui::connection::tests::esc_walks_back_and_ctrl_c_interrupts |
| TestInitWizard_ConnectionCancelledViaEsc | tui::init::tests::esc_on_the_connection_wizard_returns_to_the_setup_question; black-box terminal/init-wizard-connection-esc (PGMI-869) |
| TestInitWizard_View_TemplateStep, _RadioRowsAlign | tui::init::tests::the_template_screen_lists_every_template_with_aligned_radios |
| TestInitWizard_View_DirectoryStep, _BlockedDirectoryStaysOnTheDirectoryStep, _CheckDirBlocking | dropped from the wizard: Rust has no directory step, the target is the argument. The blocking check is commands::init::tests::git_pgmi_yaml_and_env_do_not_block, any_other_entry_blocks |
| TestInitWizard_StepsFit, _EmbeddedWizardGetsWindowSize | dropped: each wizard draws its own full screen at the terminal's size |

## Not yet mapped

Review B's m10 (PGMI-858): `internal/cli/pgpass_test.go` (15 tests),
`internal/ui/approver_test.go` (17) and `internal/cli/serve_*_test.go` (33)
have Rust counterparts only in part (`conn::pgpass`, `deploy::approve`,
`serve`); their tests still need the unit-by-unit mapping above.

## Rust fixed where the ports failed

- **Case twins in `pgmi.yaml` and `PGMI_PARAM_*`.** Rust kept whichever
  spelling it saw last; Go refuses with exit 10. `collect` now refuses them.
- **A non-UTF-8 `PGMI_PARAM_*` variable** (value or name) was silently dropped
  from the parameters; it is now refused with exit 10.
- **Server floor.** Rust had no PostgreSQL 15 check; an older server failed
  later on a missing function, possibly after creating or dropping a database.
- **Lost `CREATE DATABASE` race** was exit 13 with the catalog error; it is now
  exit 15 as in Go, and a `CREATEDB` refusal names `-d` and `CREATEDB`.
- **`COMMIT AND CHAIN` at the end of the head.** Rust counted the head as not
  committed (the session is never idle after it), so a later failure said
  "nothing committed", and a `deploy.sql` ending there failed. Both now match Go.
- **Callbacks in `CALL pgmi_test()`.** An invalid callback was exit 13 from the
  server; it is now exit 10 before the query, as in Go. `''` as pattern or
  callback means the default, as in Go (Rust sent `''` and the server refused
  the empty callback).
- **Metadata validation texts** regained Go's hints (`uuidgen`, the sort-key
  format, the documentation reference and `pgmi metadata scaffold`).

## Differences kept, reported to the owner

- **A non-ASCII character glued to `CALL`** (`—CALL pgmi_test()`): Go expands
  it; Rust does not, because PostgreSQL reads `—CALL` as one identifier. Both
  fail at the server; `testMacros` differs (1 against 0).
- **`--overwrite -d POSTGRES` with maintenance database `postgres`**: Go refuses
  (case-insensitive compare); Rust compares exactly, as PostgreSQL does for the
  quoted name it drops.
- **Provider and template lists wrap** at both ends in Rust; Go stops at the
  first and last entry.
