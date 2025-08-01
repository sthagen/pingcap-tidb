// Copyright 2025 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vardef

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	goatomic "sync/atomic"
	"time"

	"github.com/pingcap/tidb/pkg/config"
	"github.com/pingcap/tidb/pkg/executor/join/joinversion"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/util/memory"
	"github.com/pingcap/tidb/pkg/util/paging"
	"github.com/pingcap/tidb/pkg/util/size"
	"github.com/pingcap/tipb/go-tipb"
	"go.uber.org/atomic"
)

/*
	Steps to add a new TiDB specific system variable:

	1. Add a new variable name with comment in this file.
	2. Add the default value of the new variable in this file.
	3. Add SysVar instance in 'defaultSysVars' slice.
*/

// TiDB system variable names that only in session scope.
const (
	TiDBDDLSlowOprThreshold = "ddl_slow_threshold"

	// TiDBSnapshot is used for reading history data, the default value is empty string.
	// The value can be a datetime string like '2017-11-11 20:20:20' or a tso string. When this variable is set, the session reads history data of that time.
	TiDBSnapshot = "tidb_snapshot"

	// TiDBOptAggPushDown is used to enable/disable the optimizer rule of aggregation push down.
	TiDBOptAggPushDown = "tidb_opt_agg_push_down"

	// TiDBOptDeriveTopN is used to enable/disable the optimizer rule of deriving topN.
	TiDBOptDeriveTopN = "tidb_opt_derive_topn"

	// TiDBOptCartesianBCJ is used to disable/enable broadcast cartesian join in MPP mode
	TiDBOptCartesianBCJ = "tidb_opt_broadcast_cartesian_join"

	TiDBOptMPPOuterJoinFixedBuildSide = "tidb_opt_mpp_outer_join_fixed_build_side"

	// TiDBOptDistinctAggPushDown is used to decide whether agg with distinct should be pushed to tikv/tiflash.
	TiDBOptDistinctAggPushDown = "tidb_opt_distinct_agg_push_down"

	// TiDBOptSkewDistinctAgg is used to indicate the distinct agg has data skew
	TiDBOptSkewDistinctAgg = "tidb_opt_skew_distinct_agg"

	// TiDBOpt3StageDistinctAgg is used to indicate whether to plan and execute the distinct agg in 3 stages
	TiDBOpt3StageDistinctAgg = "tidb_opt_three_stage_distinct_agg"

	// TiDBOptEnable3StageMultiDistinctAgg is used to indicate whether to plan and execute the multi distinct agg in 3 stages
	TiDBOptEnable3StageMultiDistinctAgg = "tidb_opt_enable_three_stage_multi_distinct_agg"

	TiDBOptExplainNoEvaledSubQuery = "tidb_opt_enable_non_eval_scalar_subquery"

	// TiDBBCJThresholdSize is used to limit the size of small table for mpp broadcast join.
	// Its unit is bytes, if the size of small table is larger than it, we will not use bcj.
	TiDBBCJThresholdSize = "tidb_broadcast_join_threshold_size"

	// TiDBBCJThresholdCount is used to limit the count of small table for mpp broadcast join.
	// If we can't estimate the size of one side of join child, we will check if its row number exceeds this limitation.
	TiDBBCJThresholdCount = "tidb_broadcast_join_threshold_count"

	// TiDBPreferBCJByExchangeDataSize indicates the method used to choose mpp broadcast join
	TiDBPreferBCJByExchangeDataSize = "tidb_prefer_broadcast_join_by_exchange_data_size"

	// TiDBOptWriteRowID is used to enable/disable the operations of insert、replace and update to _tidb_rowid.
	TiDBOptWriteRowID = "tidb_opt_write_row_id"

	// TiDBAutoAnalyzeRatio will run if (table modify count)/(table row count) is greater than this value.
	TiDBAutoAnalyzeRatio = "tidb_auto_analyze_ratio"

	// TiDBAutoAnalyzeStartTime will run if current time is within start time and end time.
	TiDBAutoAnalyzeStartTime = "tidb_auto_analyze_start_time"
	TiDBAutoAnalyzeEndTime   = "tidb_auto_analyze_end_time"

	// TiDBChecksumTableConcurrency is used to speed up the ADMIN CHECKSUM TABLE
	// statement, when a table has multiple indices, those indices can be
	// scanned concurrently, with the cost of higher system performance impact.
	TiDBChecksumTableConcurrency = "tidb_checksum_table_concurrency"

	// TiDBCurrentTS is used to get the current transaction timestamp.
	// It is read-only.
	TiDBCurrentTS = "tidb_current_ts"

	// TiDBLastTxnInfo is used to get the last transaction info within the current session.
	TiDBLastTxnInfo = "tidb_last_txn_info"

	// TiDBLastQueryInfo is used to get the last query info within the current session.
	TiDBLastQueryInfo = "tidb_last_query_info"

	// TiDBLastDDLInfo is used to get the last ddl info within the current session.
	TiDBLastDDLInfo = "tidb_last_ddl_info"

	// TiDBLastPlanReplayerToken is used to get the last plan replayer token within the current session
	TiDBLastPlanReplayerToken = "tidb_last_plan_replayer_token"

	// TiDBConfig is a read-only variable that shows the config of the current server.
	TiDBConfig = "tidb_config"

	// TiDBBatchInsert is used to enable/disable auto-split insert data. If set this option on, insert executor will automatically
	// insert data into multiple batches and use a single txn for each batch. This will be helpful when inserting large data.
	TiDBBatchInsert = "tidb_batch_insert"

	// TiDBBatchDelete is used to enable/disable auto-split delete data. If set this option on, delete executor will automatically
	// split data into multiple batches and use a single txn for each batch. This will be helpful when deleting large data.
	TiDBBatchDelete = "tidb_batch_delete"

	// TiDBBatchCommit is used to enable/disable auto-split the transaction.
	// If set this option on, the transaction will be committed when it reaches stmt-count-limit and starts a new transaction.
	TiDBBatchCommit = "tidb_batch_commit"

	// TiDBDMLBatchSize is used to split the insert/delete data into small batches.
	// It only takes effort when tidb_batch_insert/tidb_batch_delete is on.
	// Its default value is 20000. When the row size is large, 20k rows could be larger than 100MB.
	// User could change it to a smaller one to avoid breaking the transaction size limitation.
	TiDBDMLBatchSize = "tidb_dml_batch_size"

	// The following session variables controls the memory quota during query execution.

	// TiDBMemQuotaQuery controls the memory quota of a query.
	TiDBMemQuotaQuery = "tidb_mem_quota_query" // Bytes.
	// TiDBMemQuotaApplyCache controls the memory quota of a query.
	TiDBMemQuotaApplyCache = "tidb_mem_quota_apply_cache"

	// TiDBGeneralLog is used to log every query in the server in info level.
	TiDBGeneralLog = "tidb_general_log"

	// TiDBLogFileMaxDays is used to log every query in the server in info level.
	TiDBLogFileMaxDays = "tidb_log_file_max_days"

	// TiDBPProfSQLCPU is used to add label sql label to pprof result.
	TiDBPProfSQLCPU = "tidb_pprof_sql_cpu"

	// TiDBRetryLimit is the maximum number of retries when committing a transaction.
	TiDBRetryLimit = "tidb_retry_limit"

	// TiDBDisableTxnAutoRetry disables transaction auto retry.
	// Deprecated: This variable is deprecated, please do not use this variable.
	TiDBDisableTxnAutoRetry = "tidb_disable_txn_auto_retry"

	// TiDBEnableChunkRPC enables TiDB to use Chunk format for coprocessor requests.
	TiDBEnableChunkRPC = "tidb_enable_chunk_rpc"

	// TiDBOptimizerSelectivityLevel is used to control the selectivity estimation level.
	TiDBOptimizerSelectivityLevel = "tidb_optimizer_selectivity_level"

	// TiDBOptimizerEnableNewOnlyFullGroupByCheck is used to open the newly only_full_group_by check by maintaining functional dependency.
	TiDBOptimizerEnableNewOnlyFullGroupByCheck = "tidb_enable_new_only_full_group_by_check"

	TiDBOptimizerEnableOuterJoinReorder = "tidb_enable_outer_join_reorder"

	// TiDBOptimizerEnableNAAJ is used to open the newly null-aware anti join
	TiDBOptimizerEnableNAAJ = "tidb_enable_null_aware_anti_join"

	// TiDBTxnMode is used to control the transaction behavior.
	TiDBTxnMode = "tidb_txn_mode"

	// TiDBRowFormatVersion is used to control tidb row format version current.
	TiDBRowFormatVersion = "tidb_row_format_version"

	// TiDBEnableRowLevelChecksum is used to control whether to append checksum to row values.
	TiDBEnableRowLevelChecksum = "tidb_enable_row_level_checksum"

	// TiDBEnableTablePartition is used to control table partition feature.
	// The valid value include auto/on/off:
	// on or auto: enable table partition if the partition type is implemented.
	// off: always disable table partition.
	TiDBEnableTablePartition = "tidb_enable_table_partition"

	// TiDBEnableListTablePartition is used to control list table partition feature.
	// Deprecated: This variable is deprecated, please do not use this variable.
	TiDBEnableListTablePartition = "tidb_enable_list_partition"

	// TiDBSkipIsolationLevelCheck is used to control whether to return error when set unsupported transaction
	// isolation level.
	TiDBSkipIsolationLevelCheck = "tidb_skip_isolation_level_check"

	// TiDBLowResolutionTSO is used for reading data with low resolution TSO which is updated once every two seconds
	TiDBLowResolutionTSO = "tidb_low_resolution_tso"

	// TiDBReplicaRead is used for reading data from replicas, followers for example.
	TiDBReplicaRead = "tidb_replica_read"

	// TiDBAdaptiveClosestReadThreshold is for reading data from closest replicas(with same 'zone' label).
	// TiKV client should send read request to the closest replica(leader/follower) if the estimated response
	// size exceeds this threshold; otherwise, this request should be sent to leader.
	// This variable only take effect when `tidb_replica_read` is 'closest-adaptive'.
	TiDBAdaptiveClosestReadThreshold = "tidb_adaptive_closest_read_threshold"

	// TiDBAllowRemoveAutoInc indicates whether a user can drop the auto_increment column attribute or not.
	TiDBAllowRemoveAutoInc = "tidb_allow_remove_auto_inc"

	// TiDBMultiStatementMode enables multi statement at the risk of SQL injection
	// provides backwards compatibility
	TiDBMultiStatementMode = "tidb_multi_statement_mode"

	// TiDBEvolvePlanTaskMaxTime controls the max time of a single evolution task.
	TiDBEvolvePlanTaskMaxTime = "tidb_evolve_plan_task_max_time"

	// TiDBEvolvePlanTaskStartTime is the start time of evolution task.
	TiDBEvolvePlanTaskStartTime = "tidb_evolve_plan_task_start_time"
	// TiDBEvolvePlanTaskEndTime is the end time of evolution task.
	TiDBEvolvePlanTaskEndTime = "tidb_evolve_plan_task_end_time"

	// TiDBSlowLogThreshold is used to set the slow log threshold in the server.
	TiDBSlowLogThreshold = "tidb_slow_log_threshold"

	// TiDBSlowTxnLogThreshold is used to set the slow transaction log threshold in the server.
	TiDBSlowTxnLogThreshold = "tidb_slow_txn_log_threshold"

	// TiDBRecordPlanInSlowLog is used to log the plan of the slow query.
	TiDBRecordPlanInSlowLog = "tidb_record_plan_in_slow_log"

	// TiDBEnableSlowLog enables TiDB to log slow queries.
	TiDBEnableSlowLog = "tidb_enable_slow_log"

	// TiDBCheckMb4ValueInUTF8 is used to control whether to enable the check wrong utf8 value.
	TiDBCheckMb4ValueInUTF8 = "tidb_check_mb4_value_in_utf8"

	// TiDBFoundInPlanCache indicates whether the last statement was found in plan cache
	TiDBFoundInPlanCache = "last_plan_from_cache"

	// TiDBFoundInBinding indicates whether the last statement was matched with the hints in the binding.
	TiDBFoundInBinding = "last_plan_from_binding"

	// TiDBAllowAutoRandExplicitInsert indicates whether explicit insertion on auto_random column is allowed.
	TiDBAllowAutoRandExplicitInsert = "allow_auto_random_explicit_insert"

	// TiDBTxnReadTS indicates the next transaction should be staleness transaction and provide the startTS
	TiDBTxnReadTS = "tx_read_ts"

	// TiDBReadStaleness indicates the staleness duration for following statement
	TiDBReadStaleness = "tidb_read_staleness"

	// TiDBEnablePaging indicates whether paging is enabled in coprocessor requests.
	TiDBEnablePaging = "tidb_enable_paging"

	// TiDBReadConsistency indicates whether the autocommit read statement goes through TiKV RC.
	TiDBReadConsistency = "tidb_read_consistency"

	// TiDBSysdateIsNow is the name of the `tidb_sysdate_is_now` system variable
	TiDBSysdateIsNow = "tidb_sysdate_is_now"

	// RequireSecureTransport indicates the secure mode for data transport
	RequireSecureTransport = "require_secure_transport"

	// TiFlashFastScan indicates whether use fast scan in tiflash.
	TiFlashFastScan = "tiflash_fastscan"

	// TiDBEnableUnsafeSubstitute indicates whether to enable generate column takes unsafe substitute.
	TiDBEnableUnsafeSubstitute = "tidb_enable_unsafe_substitute"

	// TiDBEnableTiFlashReadForWriteStmt indicates whether to enable TiFlash to read for write statements.
	TiDBEnableTiFlashReadForWriteStmt = "tidb_enable_tiflash_read_for_write_stmt"

	// TiDBUseAlloc indicates whether the last statement used chunk alloc
	TiDBUseAlloc = "last_sql_use_alloc"

	// TiDBExplicitRequestSourceType indicates the source of the request, it's a complement of RequestSourceType.
	// The value maybe "lightning", "br", "dumpling" etc.
	TiDBExplicitRequestSourceType = "tidb_request_source_type"
)

// TiDB system variable names that both in session and global scope.
const (
	// TiDBBuildStatsConcurrency specifies the number of concurrent workers used for analyzing tables or partitions.
	// When multiple tables or partitions are specified in the analyze statement, TiDB will process them concurrently.
	// Additionally, this setting controls the concurrency for building NDV (Number of Distinct Values) for special indexes,
	// such as generated columns composed indexes.
	TiDBBuildStatsConcurrency = "tidb_build_stats_concurrency"

	// TiDBBuildSamplingStatsConcurrency is used to control the concurrency of building stats using sampling.
	// 1. The number of concurrent workers to merge FMSketches and Sample Data from different regions.
	// 2. The number of concurrent workers to build TopN and Histogram concurrently.
	TiDBBuildSamplingStatsConcurrency = "tidb_build_sampling_stats_concurrency"

	// TiDBDistSQLScanConcurrency is used to set the concurrency of a distsql scan task.
	// A distsql scan task can be a table scan or a index scan, which may be distributed to many TiKV nodes.
	// Higher concurrency may reduce latency, but with the cost of higher memory usage and system performance impact.
	// If the query has a LIMIT clause, high concurrency makes the system do much more work than needed.
	TiDBDistSQLScanConcurrency = "tidb_distsql_scan_concurrency"

	// TiDBAnalyzeDistSQLScanConcurrency is the number of concurrent workers to scan regions to collect statistics (FMSketch, Samples).
	// For auto analyze, the value is controlled by tidb_sysproc_scan_concurrency variable.
	TiDBAnalyzeDistSQLScanConcurrency = "tidb_analyze_distsql_scan_concurrency"

	// TiDBOptInSubqToJoinAndAgg is used to enable/disable the optimizer rule of rewriting IN subquery.
	TiDBOptInSubqToJoinAndAgg = "tidb_opt_insubq_to_join_and_agg"

	// TiDBOptPreferRangeScan is used to enable/disable the optimizer to always prefer range scan over table scan, ignoring their costs.
	TiDBOptPreferRangeScan = "tidb_opt_prefer_range_scan"

	// TiDBOptEnableCorrelationAdjustment is used to indicates if enable correlation adjustment.
	TiDBOptEnableCorrelationAdjustment = "tidb_opt_enable_correlation_adjustment"

	// TiDBOptLimitPushDownThreshold determines if push Limit or TopN down to TiKV forcibly.
	TiDBOptLimitPushDownThreshold = "tidb_opt_limit_push_down_threshold"

	// TiDBOptCorrelationThreshold is a guard to enable row count estimation using column order correlation.
	TiDBOptCorrelationThreshold = "tidb_opt_correlation_threshold"

	// TiDBOptCorrelationExpFactor is an exponential factor to control heuristic approach when tidb_opt_correlation_threshold is not satisfied.
	TiDBOptCorrelationExpFactor = "tidb_opt_correlation_exp_factor"

	// TiDBOptRiskEqSkewRatio controls the amount of skew is applied to equal predicate estimation when a value is not found in TopN/buckets.
	TiDBOptRiskEqSkewRatio = "tidb_opt_risk_eq_skew_ratio"

	// TiDBOptRiskRangeSkewRatio controls the amount of skew that is applied to range predicate estimation when a range falls within a bucket or outside the histogram bucket range.
	TiDBOptRiskRangeSkewRatio = "tidb_opt_risk_range_skew_ratio"

	// TiDBOptCPUFactor is the CPU cost of processing one expression for one row.
	TiDBOptCPUFactor = "tidb_opt_cpu_factor"
	// TiDBOptCopCPUFactor is the CPU cost of processing one expression for one row in coprocessor.
	TiDBOptCopCPUFactor = "tidb_opt_copcpu_factor"
	// TiDBOptTiFlashConcurrencyFactor is concurrency number of tiflash computation.
	TiDBOptTiFlashConcurrencyFactor = "tidb_opt_tiflash_concurrency_factor"
	// TiDBOptNetworkFactor is the network cost of transferring 1 byte data.
	TiDBOptNetworkFactor = "tidb_opt_network_factor"
	// TiDBOptScanFactor is the IO cost of scanning 1 byte data on TiKV.
	TiDBOptScanFactor = "tidb_opt_scan_factor"
	// TiDBOptDescScanFactor is the IO cost of scanning 1 byte data on TiKV in desc order.
	TiDBOptDescScanFactor = "tidb_opt_desc_factor"
	// TiDBOptSeekFactor is the IO cost of seeking the start value in a range on TiKV or TiFlash.
	TiDBOptSeekFactor = "tidb_opt_seek_factor"
	// TiDBOptMemoryFactor is the memory cost of storing one tuple.
	TiDBOptMemoryFactor = "tidb_opt_memory_factor"
	// TiDBOptDiskFactor is the IO cost of reading/writing one byte to temporary disk.
	TiDBOptDiskFactor = "tidb_opt_disk_factor"
	// TiDBOptConcurrencyFactor is the CPU cost of additional one goroutine.
	TiDBOptConcurrencyFactor = "tidb_opt_concurrency_factor"

	// The following optimizer cost factors represent a multiplier for each optimizer physical operator.
	// These factors are used to adjust the cost of each operator to influence the optimizer's plan selection.
	TiDBOptIndexScanCostFactor        = "tidb_opt_index_scan_cost_factor"
	TiDBOptIndexReaderCostFactor      = "tidb_opt_index_reader_cost_factor"
	TiDBOptTableReaderCostFactor      = "tidb_opt_table_reader_cost_factor"
	TiDBOptTableFullScanCostFactor    = "tidb_opt_table_full_scan_cost_factor"
	TiDBOptTableRangeScanCostFactor   = "tidb_opt_table_range_scan_cost_factor"
	TiDBOptTableRowIDScanCostFactor   = "tidb_opt_table_rowid_scan_cost_factor"
	TiDBOptTableTiFlashScanCostFactor = "tidb_opt_table_tiflash_scan_cost_factor"
	TiDBOptIndexLookupCostFactor      = "tidb_opt_index_lookup_cost_factor"
	TiDBOptIndexMergeCostFactor       = "tidb_opt_index_merge_cost_factor"
	TiDBOptSortCostFactor             = "tidb_opt_sort_cost_factor"
	TiDBOptTopNCostFactor             = "tidb_opt_topn_cost_factor"
	TiDBOptLimitCostFactor            = "tidb_opt_limit_cost_factor"
	TiDBOptStreamAggCostFactor        = "tidb_opt_stream_agg_cost_factor"
	TiDBOptHashAggCostFactor          = "tidb_opt_hash_agg_cost_factor"
	TiDBOptMergeJoinCostFactor        = "tidb_opt_merge_join_cost_factor"
	TiDBOptHashJoinCostFactor         = "tidb_opt_hash_join_cost_factor"
	TiDBOptIndexJoinCostFactor        = "tidb_opt_index_join_cost_factor"

	// TiDBOptForceInlineCTE is used to enable/disable inline CTE
	TiDBOptForceInlineCTE = "tidb_opt_force_inline_cte"

	// TiDBIndexJoinBatchSize is used to set the batch size of an index lookup join.
	// The index lookup join fetches batches of data from outer executor and constructs ranges for inner executor.
	// This value controls how much of data in a batch to do the index join.
	// Large value may reduce the latency but consumes more system resource.
	TiDBIndexJoinBatchSize = "tidb_index_join_batch_size"

	// TiDBIndexLookupSize is used for index lookup executor.
	// The index lookup executor first scan a batch of handles from a index, then use those handles to lookup the table
	// rows, this value controls how much of handles in a batch to do a lookup task.
	// Small value sends more RPCs to TiKV, consume more system resource.
	// Large value may do more work than needed if the query has a limit.
	TiDBIndexLookupSize = "tidb_index_lookup_size"

	// TiDBIndexLookupConcurrency is used for index lookup executor.
	// A lookup task may have 'tidb_index_lookup_size' of handles at maximum, the handles may be distributed
	// in many TiKV nodes, we execute multiple concurrent index lookup tasks concurrently to reduce the time
	// waiting for a task to finish.
	// Set this value higher may reduce the latency but consumes more system resource.
	// tidb_index_lookup_concurrency is deprecated, use tidb_executor_concurrency instead.
	TiDBIndexLookupConcurrency = "tidb_index_lookup_concurrency"

	// TiDBIndexLookupJoinConcurrency is used for index lookup join executor.
	// IndexLookUpJoin starts "tidb_index_lookup_join_concurrency" inner workers
	// to fetch inner rows and join the matched (outer, inner) row pairs.
	// tidb_index_lookup_join_concurrency is deprecated, use tidb_executor_concurrency instead.
	TiDBIndexLookupJoinConcurrency = "tidb_index_lookup_join_concurrency"

	// TiDBIndexSerialScanConcurrency is used for controlling the concurrency of index scan operation
	// when we need to keep the data output order the same as the order of index data.
	TiDBIndexSerialScanConcurrency = "tidb_index_serial_scan_concurrency"

	// TiDBMaxChunkSize is used to control the max chunk size during query execution.
	TiDBMaxChunkSize = "tidb_max_chunk_size"

	// TiDBAllowBatchCop means if we should send batch coprocessor to TiFlash. It can be set to 0, 1 and 2.
	// 0 means never use batch cop, 1 means use batch cop in case of aggregation and join, 2, means to force sending batch cop for any query.
	// The default value is 0
	TiDBAllowBatchCop = "tidb_allow_batch_cop"

	// TiDBShardRowIDBits means all the tables created in the current session will be sharded.
	// The default value is 0
	TiDBShardRowIDBits = "tidb_shard_row_id_bits"

	// TiDBPreSplitRegions means all the tables created in the current session will be pre-splited.
	// The default value is 0
	TiDBPreSplitRegions = "tidb_pre_split_regions"

	// TiDBAllowMPPExecution means if we should use mpp way to execute query or not.
	// Default value is `true`, means to be determined by the optimizer.
	// Value set to `false` means never use mpp.
	TiDBAllowMPPExecution = "tidb_allow_mpp"

	// TiDBAllowTiFlashCop means we only use MPP mode to query data.
	// Default value is `true`, means to be determined by the optimizer.
	// Value set to `false` means we may fall back to TiFlash cop plan if possible.
	TiDBAllowTiFlashCop = "tidb_allow_tiflash_cop"

	// TiDBHashExchangeWithNewCollation means if hash exchange is supported when new collation is on.
	// Default value is `true`, means support hash exchange when new collation is on.
	// Value set to `false` means not support hash exchange when new collation is on.
	TiDBHashExchangeWithNewCollation = "tidb_hash_exchange_with_new_collation"

	// TiDBEnforceMPPExecution means if we should enforce mpp way to execute query or not.
	// Default value is `false`, means to be determined by variable `tidb_allow_mpp`.
	// Value set to `true` means enforce use mpp.
	// Note if you want to set `tidb_enforce_mpp` to `true`, you must set `tidb_allow_mpp` to `true` first.
	TiDBEnforceMPPExecution = "tidb_enforce_mpp"

	// TiDBMaxTiFlashThreads is the maximum number of threads to execute the request which is pushed down to tiflash.
	// Default value is -1, means it will not be pushed down to tiflash.
	// If the value is bigger than -1, it will be pushed down to tiflash and used to create db context in tiflash.
	TiDBMaxTiFlashThreads = "tidb_max_tiflash_threads"

	// TiDBMaxBytesBeforeTiFlashExternalJoin is the maximum bytes used by a TiFlash join before spill to disk
	TiDBMaxBytesBeforeTiFlashExternalJoin = "tidb_max_bytes_before_tiflash_external_join"

	// TiDBMaxBytesBeforeTiFlashExternalGroupBy is the maximum bytes used by a TiFlash hash aggregation before spill to disk
	TiDBMaxBytesBeforeTiFlashExternalGroupBy = "tidb_max_bytes_before_tiflash_external_group_by"

	// TiDBMaxBytesBeforeTiFlashExternalSort is the maximum bytes used by a TiFlash sort/TopN before spill to disk
	TiDBMaxBytesBeforeTiFlashExternalSort = "tidb_max_bytes_before_tiflash_external_sort"

	// TiFlashMemQuotaQueryPerNode is the maximum bytes used by a TiFlash Query on each TiFlash node
	TiFlashMemQuotaQueryPerNode = "tiflash_mem_quota_query_per_node"

	// TiFlashQuerySpillRatio is the threshold that TiFlash will trigger auto spill when the memory usage is above this percentage
	TiFlashQuerySpillRatio = "tiflash_query_spill_ratio"

	// TiFlashHashJoinVersion indicates whether to use hash join implementation v2 in TiFlash.
	TiFlashHashJoinVersion = "tiflash_hash_join_version"

	// TiDBMPPStoreFailTTL is the unavailable time when a store is detected failed. During that time, tidb will not send any task to
	// TiFlash even though the failed TiFlash node has been recovered.
	TiDBMPPStoreFailTTL = "tidb_mpp_store_fail_ttl"

	// TiDBInitChunkSize is used to control the init chunk size during query execution.
	TiDBInitChunkSize = "tidb_init_chunk_size"

	// TiDBMinPagingSize is used to control the min paging size in the coprocessor paging protocol.
	TiDBMinPagingSize = "tidb_min_paging_size"

	// TiDBMaxPagingSize is used to control the max paging size in the coprocessor paging protocol.
	TiDBMaxPagingSize = "tidb_max_paging_size"

	// TiDBEnableCascadesPlanner is used to control whether to enable the cascades planner.
	TiDBEnableCascadesPlanner = "tidb_enable_cascades_planner"

	// TiDBSkipUTF8Check skips the UTF8 validate process, validate UTF8 has performance cost, if we can make sure
	// the input string values are valid, we can skip the check.
	TiDBSkipUTF8Check = "tidb_skip_utf8_check"

	// TiDBSkipASCIICheck skips the ASCII validate process
	// old tidb may already have fields with invalid ASCII bytes
	// disable ASCII validate can guarantee a safe replication
	TiDBSkipASCIICheck = "tidb_skip_ascii_check"

	// TiDBHashJoinConcurrency is used for hash join executor.
	// The hash join outer executor starts multiple concurrent join workers to probe the hash table.
	// tidb_hash_join_concurrency is deprecated, use tidb_executor_concurrency instead.
	TiDBHashJoinConcurrency = "tidb_hash_join_concurrency"

	// TiDBProjectionConcurrency is used for projection operator.
	// This variable controls the worker number of projection operator.
	// tidb_projection_concurrency is deprecated, use tidb_executor_concurrency instead.
	TiDBProjectionConcurrency = "tidb_projection_concurrency"

	// TiDBHashAggPartialConcurrency is used for hash agg executor.
	// The hash agg executor starts multiple concurrent partial workers to do partial aggregate works.
	// tidb_hashagg_partial_concurrency is deprecated, use tidb_executor_concurrency instead.
	TiDBHashAggPartialConcurrency = "tidb_hashagg_partial_concurrency"

	// TiDBHashAggFinalConcurrency is used for hash agg executor.
	// The hash agg executor starts multiple concurrent final workers to do final aggregate works.
	// tidb_hashagg_final_concurrency is deprecated, use tidb_executor_concurrency instead.
	TiDBHashAggFinalConcurrency = "tidb_hashagg_final_concurrency"

	// TiDBWindowConcurrency is used for window parallel executor.
	// tidb_window_concurrency is deprecated, use tidb_executor_concurrency instead.
	TiDBWindowConcurrency = "tidb_window_concurrency"

	// TiDBMergeJoinConcurrency is used for merge join parallel executor
	TiDBMergeJoinConcurrency = "tidb_merge_join_concurrency"

	// TiDBStreamAggConcurrency is used for stream aggregation parallel executor.
	// tidb_stream_agg_concurrency is deprecated, use tidb_executor_concurrency instead.
	TiDBStreamAggConcurrency = "tidb_streamagg_concurrency"

	// TiDBIndexMergeIntersectionConcurrency is used for parallel worker of index merge intersection.
	TiDBIndexMergeIntersectionConcurrency = "tidb_index_merge_intersection_concurrency"

	// TiDBEnableParallelApply is used for parallel apply.
	TiDBEnableParallelApply = "tidb_enable_parallel_apply"

	// TiDBBackoffLockFast is used for tikv backoff base time in milliseconds.
	TiDBBackoffLockFast = "tidb_backoff_lock_fast"

	// TiDBBackOffWeight is used to control the max back off time in TiDB.
	// The default maximum back off time is a small value.
	// BackOffWeight could multiply it to let the user adjust the maximum time for retrying.
	// Only positive integers can be accepted, which means that the maximum back off time can only grow.
	TiDBBackOffWeight = "tidb_backoff_weight"

	// TiDBDDLReorgWorkerCount defines the count of ddl reorg workers.
	TiDBDDLReorgWorkerCount = "tidb_ddl_reorg_worker_cnt"

	// TiDBDDLFlashbackConcurrency defines the count of ddl flashback workers.
	TiDBDDLFlashbackConcurrency = "tidb_ddl_flashback_concurrency"

	// TiDBDDLReorgBatchSize defines the transaction batch size of ddl reorg workers.
	TiDBDDLReorgBatchSize = "tidb_ddl_reorg_batch_size"

	// TiDBDDLErrorCountLimit defines the count of ddl error limit.
	TiDBDDLErrorCountLimit = "tidb_ddl_error_count_limit"

	// TiDBDDLReorgPriority defines the operations' priority of adding indices.
	// It can be: PRIORITY_LOW, PRIORITY_NORMAL, PRIORITY_HIGH
	TiDBDDLReorgPriority = "tidb_ddl_reorg_priority"

	// TiDBDDLReorgMaxWriteSpeed defines the max write limitation for the lightning local backend
	TiDBDDLReorgMaxWriteSpeed = "tidb_ddl_reorg_max_write_speed"

	// TiDBEnableAutoIncrementInGenerated disables the mysql compatibility check on using auto-incremented columns in
	// expression indexes and generated columns described here https://dev.mysql.com/doc/refman/5.7/en/create-table-generated-columns.html for details.
	TiDBEnableAutoIncrementInGenerated = "tidb_enable_auto_increment_in_generated"

	// TiDBEnablePointGetCache is used to control whether to enable the point get cache for special scenario.
	TiDBEnablePointGetCache = "tidb_enable_point_get_cache"

	// TiDBPlacementMode is used to control the mode for placement
	TiDBPlacementMode = "tidb_placement_mode"

	// TiDBMaxDeltaSchemaCount defines the max length of deltaSchemaInfos.
	// deltaSchemaInfos is a queue that maintains the history of schema changes.
	TiDBMaxDeltaSchemaCount = "tidb_max_delta_schema_count"

	// TiDBScatterRegion will scatter the regions for DDLs when it is "table" or "global", "" indicates not trigger scatter.
	TiDBScatterRegion = "tidb_scatter_region"

	// TiDBWaitSplitRegionFinish defines the split region behaviour is sync or async.
	TiDBWaitSplitRegionFinish = "tidb_wait_split_region_finish"

	// TiDBWaitSplitRegionTimeout uses to set the split and scatter region back off time.
	TiDBWaitSplitRegionTimeout = "tidb_wait_split_region_timeout"

	// TiDBForcePriority defines the operations' priority of all statements.
	// It can be "NO_PRIORITY", "LOW_PRIORITY", "HIGH_PRIORITY", "DELAYED"
	TiDBForcePriority = "tidb_force_priority"

	// TiDBConstraintCheckInPlace indicates to check the constraint when the SQL executing.
	// It could hurt the performance of bulking insert when it is ON.
	TiDBConstraintCheckInPlace = "tidb_constraint_check_in_place"

	// TiDBEnableWindowFunction is used to control whether to enable the window function.
	TiDBEnableWindowFunction = "tidb_enable_window_function"

	// TiDBEnablePipelinedWindowFunction is used to control whether to use pipelined window function, it only works when tidb_enable_window_function = true.
	TiDBEnablePipelinedWindowFunction = "tidb_enable_pipelined_window_function"

	// TiDBEnableStrictDoubleTypeCheck is used to control table field double type syntax check.
	TiDBEnableStrictDoubleTypeCheck = "tidb_enable_strict_double_type_check"

	// TiDBOptProjectionPushDown is used to control whether to pushdown projection to coprocessor.
	TiDBOptProjectionPushDown = "tidb_opt_projection_push_down"

	// TiDBEnableVectorizedExpression is used to control whether to enable the vectorized expression evaluation.
	TiDBEnableVectorizedExpression = "tidb_enable_vectorized_expression"

	// TiDBOptJoinReorderThreshold defines the threshold less than which
	// we'll choose a rather time-consuming algorithm to calculate the join order.
	TiDBOptJoinReorderThreshold = "tidb_opt_join_reorder_threshold"

	// TiDBSlowQueryFile indicates which slow query log file for SLOW_QUERY table to parse.
	TiDBSlowQueryFile = "tidb_slow_query_file"

	// TiDBEnableFastAnalyze indicates to use fast analyze.
	// Deprecated: This variable is deprecated, please do not use this variable.
	TiDBEnableFastAnalyze = "tidb_enable_fast_analyze"

	// TiDBExpensiveQueryTimeThreshold indicates the time threshold of expensive query.
	TiDBExpensiveQueryTimeThreshold = "tidb_expensive_query_time_threshold"

	// TiDBExpensiveTxnTimeThreshold indicates the time threshold of expensive transaction.
	TiDBExpensiveTxnTimeThreshold = "tidb_expensive_txn_time_threshold"

	// TiDBEnableIndexMerge indicates to generate IndexMergePath.
	TiDBEnableIndexMerge = "tidb_enable_index_merge"

	// TiDBEnableNoopFuncs set true will enable using fake funcs(like get_lock release_lock)
	TiDBEnableNoopFuncs = "tidb_enable_noop_functions"

	// TiDBEnableStmtSummary indicates whether the statement summary is enabled.
	TiDBEnableStmtSummary = "tidb_enable_stmt_summary"

	// TiDBStmtSummaryInternalQuery indicates whether the statement summary contain internal query.
	TiDBStmtSummaryInternalQuery = "tidb_stmt_summary_internal_query"

	// TiDBStmtSummaryRefreshInterval indicates the refresh interval in seconds for each statement summary.
	TiDBStmtSummaryRefreshInterval = "tidb_stmt_summary_refresh_interval"

	// TiDBStmtSummaryHistorySize indicates the history size of each statement summary.
	TiDBStmtSummaryHistorySize = "tidb_stmt_summary_history_size"

	// TiDBStmtSummaryMaxStmtCount indicates the max number of statements kept in memory.
	TiDBStmtSummaryMaxStmtCount = "tidb_stmt_summary_max_stmt_count"

	// TiDBStmtSummaryMaxSQLLength indicates the max length of displayed normalized sql and sample sql.
	TiDBStmtSummaryMaxSQLLength = "tidb_stmt_summary_max_sql_length"

	// TiDBIgnoreInlistPlanDigest enables TiDB to generate the same plan digest with SQL using different in-list arguments.
	TiDBIgnoreInlistPlanDigest = "tidb_ignore_inlist_plan_digest"

	// TiDBCapturePlanBaseline indicates whether the capture of plan baselines is enabled.
	TiDBCapturePlanBaseline = "tidb_capture_plan_baselines"

	// TiDBUsePlanBaselines indicates whether the use of plan baselines is enabled.
	TiDBUsePlanBaselines = "tidb_use_plan_baselines"

	// TiDBEvolvePlanBaselines indicates whether the evolution of plan baselines is enabled.
	TiDBEvolvePlanBaselines = "tidb_evolve_plan_baselines"

	// TiDBOptEnableFuzzyBinding indicates whether to enable the universal binding.
	TiDBOptEnableFuzzyBinding = "tidb_opt_enable_fuzzy_binding"

	// TiDBEnableExtendedStats indicates whether the extended statistics feature is enabled.
	TiDBEnableExtendedStats = "tidb_enable_extended_stats"

	// TiDBIsolationReadEngines indicates the tidb only read from the stores whose engine type is involved in IsolationReadEngines.
	// Now, only support TiKV and TiFlash.
	TiDBIsolationReadEngines = "tidb_isolation_read_engines"

	// TiDBStoreLimit indicates the limit of sending request to a store, 0 means without limit.
	TiDBStoreLimit = "tidb_store_limit"

	// TiDBMetricSchemaStep indicates the step when query metric schema.
	TiDBMetricSchemaStep = "tidb_metric_query_step"

	// TiDBCDCWriteSource indicates the following data is written by TiCDC if it is not 0.
	TiDBCDCWriteSource = "tidb_cdc_write_source"

	// TiDBMetricSchemaRangeDuration indicates the range duration when query metric schema.
	TiDBMetricSchemaRangeDuration = "tidb_metric_query_range_duration"

	// TiDBEnableCollectExecutionInfo indicates that whether execution info is collected.
	TiDBEnableCollectExecutionInfo = "tidb_enable_collect_execution_info"

	// TiDBExecutorConcurrency is used for controlling the concurrency of all types of executors.
	TiDBExecutorConcurrency = "tidb_executor_concurrency"

	// TiDBEnableClusteredIndex indicates if clustered index feature is enabled.
	TiDBEnableClusteredIndex = "tidb_enable_clustered_index"

	// TiDBEnableGlobalIndex means if we could create an global index on a partition table or not.
	// Deprecated, will always be ON
	TiDBEnableGlobalIndex = "tidb_enable_global_index"

	// TiDBPartitionPruneMode indicates the partition prune mode used.
	TiDBPartitionPruneMode = "tidb_partition_prune_mode"

	// TiDBRedactLog indicates that whether redact log.
	TiDBRedactLog = "tidb_redact_log"

	// TiDBRestrictedReadOnly is meant for the cloud admin to toggle the cluster read only
	TiDBRestrictedReadOnly = "tidb_restricted_read_only"

	// TiDBSuperReadOnly is tidb's variant of mysql's super_read_only, which has some differences from mysql's super_read_only.
	TiDBSuperReadOnly = "tidb_super_read_only"

	// TiDBShardAllocateStep indicates the max size of continuous rowid shard in one transaction.
	TiDBShardAllocateStep = "tidb_shard_allocate_step"
	// TiDBEnableTelemetry indicates that whether usage data report to PingCAP is enabled.
	// Deprecated: it is 'off' always since Telemetry has been removed from TiDB.
	TiDBEnableTelemetry = "tidb_enable_telemetry"

	// TiDBMemoryUsageAlarmRatio indicates the alarm threshold when memory usage of the tidb-server exceeds.
	TiDBMemoryUsageAlarmRatio = "tidb_memory_usage_alarm_ratio"

	// TiDBMemoryUsageAlarmKeepRecordNum indicates the number of saved alarm files.
	TiDBMemoryUsageAlarmKeepRecordNum = "tidb_memory_usage_alarm_keep_record_num"

	// TiDBEnableRateLimitAction indicates whether enabled ratelimit action
	TiDBEnableRateLimitAction = "tidb_enable_rate_limit_action"

	// TiDBEnableAsyncCommit indicates whether to enable the async commit feature.
	TiDBEnableAsyncCommit = "tidb_enable_async_commit"

	// TiDBEnable1PC indicates whether to enable the one-phase commit feature.
	TiDBEnable1PC = "tidb_enable_1pc"

	// TiDBGuaranteeLinearizability indicates whether to guarantee linearizability.
	TiDBGuaranteeLinearizability = "tidb_guarantee_linearizability"

	// TiDBAnalyzeVersion indicates how tidb collects the analyzed statistics and how use to it.
	TiDBAnalyzeVersion = "tidb_analyze_version"

	// TiDBAutoAnalyzePartitionBatchSize indicates the batch size for partition tables for auto analyze in dynamic mode
	// Deprecated: This variable is deprecated, please do not use this variable.
	TiDBAutoAnalyzePartitionBatchSize = "tidb_auto_analyze_partition_batch_size"

	// TiDBEnableIndexMergeJoin indicates whether to enable index merge join.
	TiDBEnableIndexMergeJoin = "tidb_enable_index_merge_join"

	// TiDBTrackAggregateMemoryUsage indicates whether track the memory usage of aggregate function.
	TiDBTrackAggregateMemoryUsage = "tidb_track_aggregate_memory_usage"

	// TiDBEnableExchangePartition indicates whether to enable exchange partition.
	TiDBEnableExchangePartition = "tidb_enable_exchange_partition"

	// TiDBAllowFallbackToTiKV indicates the engine types whose unavailability triggers fallback to TiKV.
	// Now we only support TiFlash.
	TiDBAllowFallbackToTiKV = "tidb_allow_fallback_to_tikv"

	// TiDBEnableTopSQL indicates whether the top SQL is enabled.
	TiDBEnableTopSQL = "tidb_enable_top_sql"

	// TiDBSourceID indicates the source ID of the TiDB server.
	TiDBSourceID = "tidb_source_id"

	// TiDBTopSQLMaxTimeSeriesCount indicates the max number of statements been collected in each time series.
	TiDBTopSQLMaxTimeSeriesCount = "tidb_top_sql_max_time_series_count"

	// TiDBTopSQLMaxMetaCount indicates the max capacity of the collect meta per second.
	TiDBTopSQLMaxMetaCount = "tidb_top_sql_max_meta_count"

	// TiDBEnableLocalTxn indicates whether to enable Local Txn.
	TiDBEnableLocalTxn = "tidb_enable_local_txn"

	// TiDBEnableMDL indicates whether to enable MDL.
	TiDBEnableMDL = "tidb_enable_metadata_lock"

	// TiDBTSOClientBatchMaxWaitTime indicates the max value of the TSO Batch Wait interval time of PD client.
	TiDBTSOClientBatchMaxWaitTime = "tidb_tso_client_batch_max_wait_time"

	// TiDBTxnCommitBatchSize is used to control the batch size of transaction commit related requests sent by TiDB to TiKV.
	// If a single transaction has a large amount of writes, you can increase the batch size to improve the batch effect,
	// setting too large will exceed TiKV's raft-entry-max-size limit and cause commit failure.
	TiDBTxnCommitBatchSize = "tidb_txn_commit_batch_size"

	// TiDBEnableTSOFollowerProxy indicates whether to enable the TSO Follower Proxy feature of PD client.
	TiDBEnableTSOFollowerProxy = "tidb_enable_tso_follower_proxy"

	// PDEnableFollowerHandleRegion indicates whether to enable the PD Follower handle region API.
	// TODO: deprecated this variable to use a format like `tidb_enable_pd_follower_handle_region`.
	PDEnableFollowerHandleRegion = "pd_enable_follower_handle_region"

	// TiDBEnableBatchQueryRegion indicates whether to enable the batch query region feature.
	TiDBEnableBatchQueryRegion = "tidb_enable_batch_query_region"

	// TiDBEnableOrderedResultMode indicates if stabilize query results.
	TiDBEnableOrderedResultMode = "tidb_enable_ordered_result_mode"

	// TiDBRemoveOrderbyInSubquery indicates whether to remove ORDER BY in subquery.
	TiDBRemoveOrderbyInSubquery = "tidb_remove_orderby_in_subquery"

	// TiDBEnablePseudoForOutdatedStats indicates whether use pseudo for outdated stats
	TiDBEnablePseudoForOutdatedStats = "tidb_enable_pseudo_for_outdated_stats"

	// TiDBRegardNULLAsPoint indicates whether regard NULL as point when optimizing
	TiDBRegardNULLAsPoint = "tidb_regard_null_as_point"

	// TiDBTmpTableMaxSize indicates the max memory size of temporary tables.
	TiDBTmpTableMaxSize = "tidb_tmp_table_max_size"

	// TiDBEnableLegacyInstanceScope indicates if instance scope can be set with SET SESSION.
	TiDBEnableLegacyInstanceScope = "tidb_enable_legacy_instance_scope"

	// TiDBTableCacheLease indicates the read lock lease of a cached table.
	TiDBTableCacheLease = "tidb_table_cache_lease"

	// TiDBStatsLoadSyncWait indicates the time sql execution will sync-wait for stats load.
	TiDBStatsLoadSyncWait = "tidb_stats_load_sync_wait"

	// TiDBEnableMutationChecker indicates whether to check data consistency for mutations
	TiDBEnableMutationChecker = "tidb_enable_mutation_checker"
	// TiDBTxnAssertionLevel indicates how strict the assertion will be, which helps to detect and preventing data &
	// index inconsistency problems.
	TiDBTxnAssertionLevel = "tidb_txn_assertion_level"

	// TiDBIgnorePreparedCacheCloseStmt indicates whether to ignore close-stmt commands for prepared statements.
	TiDBIgnorePreparedCacheCloseStmt = "tidb_ignore_prepared_cache_close_stmt"

	// TiDBEnableNewCostInterface is a internal switch to indicates whether to use the new cost calculation interface.
	TiDBEnableNewCostInterface = "tidb_enable_new_cost_interface"

	// TiDBCostModelVersion is a internal switch to indicates the cost model version.
	TiDBCostModelVersion = "tidb_cost_model_version"

	// TiDBIndexJoinDoubleReadPenaltyCostRate indicates whether to add some penalty cost to IndexJoin and how much of it.
	// IndexJoin can cause plenty of extra double read tasks, which consume lots of resources and take a long time.
	// Since the number of double read tasks is hard to estimated accurately, we leave this variable to let us can adjust this
	// part of cost manually.
	TiDBIndexJoinDoubleReadPenaltyCostRate = "tidb_index_join_double_read_penalty_cost_rate"

	// TiDBBatchPendingTiFlashCount indicates the maximum count of non-available TiFlash tables.
	TiDBBatchPendingTiFlashCount = "tidb_batch_pending_tiflash_count"

	// TiDBQueryLogMaxLen is used to set the max length of the query in the log.
	TiDBQueryLogMaxLen = "tidb_query_log_max_len"

	// TiDBEnableNoopVariables is used to indicate if noops appear in SHOW [GLOBAL] VARIABLES
	TiDBEnableNoopVariables = "tidb_enable_noop_variables"

	// TiDBNonTransactionalIgnoreError is used to ignore error in non-transactional DMLs.
	// When set to false, a non-transactional DML returns when it meets the first error.
	// When set to true, a non-transactional DML finishes all batches even if errors are met in some batches.
	TiDBNonTransactionalIgnoreError = "tidb_nontransactional_ignore_error"

	// Fine grained shuffle is disabled when TiFlashFineGrainedShuffleStreamCount is zero.
	TiFlashFineGrainedShuffleStreamCount = "tiflash_fine_grained_shuffle_stream_count"
	TiFlashFineGrainedShuffleBatchSize   = "tiflash_fine_grained_shuffle_batch_size"

	// TiDBSimplifiedMetrics controls whether to unregister some unused metrics.
	TiDBSimplifiedMetrics = "tidb_simplified_metrics"

	// TiDBMemoryDebugModeMinHeapInUse is used to set tidb memory debug mode trigger threshold.
	// When set to 0, the function is disabled.
	// When set to a negative integer, use memory debug mode to detect the issue of frequent allocation and release of memory.
	// We do not actively trigger gc, and check whether the `tracker memory * (1+bias ratio) > heap in use` each 5s.
	// When set to a positive integer, use memory debug mode to detect the issue of memory tracking inaccurate.
	// We trigger runtime.GC() each 5s, and check whether the `tracker memory * (1+bias ratio) > heap in use`.
	TiDBMemoryDebugModeMinHeapInUse = "tidb_memory_debug_mode_min_heap_inuse"
	// TiDBMemoryDebugModeAlarmRatio is used set tidb memory debug mode bias ratio. Treat memory bias less than this ratio as noise.
	TiDBMemoryDebugModeAlarmRatio = "tidb_memory_debug_mode_alarm_ratio"

	// TiDBEnableAnalyzeSnapshot indicates whether to read data on snapshot when collecting statistics.
	// When set to false, ANALYZE reads the latest data.
	// When set to true, ANALYZE reads data on the snapshot at the beginning of ANALYZE.
	TiDBEnableAnalyzeSnapshot = "tidb_enable_analyze_snapshot"

	// TiDBDefaultStrMatchSelectivity controls some special cardinality estimation strategy for string match functions (like and regexp).
	// When set to 0, Selectivity() will try to evaluate those functions with TopN and NULL in the stats to estimate,
	// and the default selectivity and the selectivity for the histogram part will be 0.1.
	// When set to (0, 1], Selectivity() will use the value of this variable as the default selectivity of those
	// functions instead of the selectionFactor (0.8).
	TiDBDefaultStrMatchSelectivity = "tidb_default_string_match_selectivity"

	// TiDBEnablePrepPlanCache indicates whether to enable prepared plan cache
	TiDBEnablePrepPlanCache = "tidb_enable_prepared_plan_cache"
	// TiDBPrepPlanCacheSize indicates the number of cached statements.
	// This variable is deprecated, use tidb_session_plan_cache_size instead.
	TiDBPrepPlanCacheSize = "tidb_prepared_plan_cache_size"
	// TiDBEnablePrepPlanCacheMemoryMonitor indicates whether to enable prepared plan cache monitor
	TiDBEnablePrepPlanCacheMemoryMonitor = "tidb_enable_prepared_plan_cache_memory_monitor"

	// TiDBEnableNonPreparedPlanCache indicates whether to enable non-prepared plan cache.
	TiDBEnableNonPreparedPlanCache = "tidb_enable_non_prepared_plan_cache"
	// TiDBEnableNonPreparedPlanCacheForDML indicates whether to enable non-prepared plan cache for DML statements.
	TiDBEnableNonPreparedPlanCacheForDML = "tidb_enable_non_prepared_plan_cache_for_dml"
	// TiDBNonPreparedPlanCacheSize controls the size of non-prepared plan cache.
	// This variable is deprecated, use tidb_session_plan_cache_size instead.
	TiDBNonPreparedPlanCacheSize = "tidb_non_prepared_plan_cache_size"
	// TiDBPlanCacheMaxPlanSize controls the maximum size of a plan that can be cached.
	TiDBPlanCacheMaxPlanSize = "tidb_plan_cache_max_plan_size"
	// TiDBPlanCacheInvalidationOnFreshStats controls if plan cache will be invalidated automatically when
	// related stats are analyzed after the plan cache is generated.
	TiDBPlanCacheInvalidationOnFreshStats = "tidb_plan_cache_invalidation_on_fresh_stats"
	// TiDBSessionPlanCacheSize controls the size of session plan cache.
	TiDBSessionPlanCacheSize = "tidb_session_plan_cache_size"

	// TiDBEnableInstancePlanCache indicates whether to enable instance plan cache.
	// If this variable is false, session-level plan cache will be used.
	TiDBEnableInstancePlanCache = "tidb_enable_instance_plan_cache"
	// TiDBInstancePlanCacheReservedPercentage indicates the percentage memory to evict.
	TiDBInstancePlanCacheReservedPercentage = "tidb_instance_plan_cache_reserved_percentage"
	// TiDBInstancePlanCacheMaxMemSize indicates the maximum memory size of instance plan cache.
	TiDBInstancePlanCacheMaxMemSize = "tidb_instance_plan_cache_max_size"

	// TiDBConstraintCheckInPlacePessimistic controls whether to skip certain kinds of pessimistic locks.
	TiDBConstraintCheckInPlacePessimistic = "tidb_constraint_check_in_place_pessimistic"

	// TiDBEnableForeignKey indicates whether to enable foreign key feature.
	// TODO(crazycs520): remove this after foreign key GA.
	TiDBEnableForeignKey = "tidb_enable_foreign_key"

	// TiDBOptRangeMaxSize is the max memory limit for ranges. When the optimizer estimates that the memory usage of complete
	// ranges would exceed the limit, it chooses less accurate ranges such as full range. 0 indicates that there is no memory
	// limit for ranges.
	TiDBOptRangeMaxSize = "tidb_opt_range_max_size"

	// TiDBOptAdvancedJoinHint indicates whether the join method hint is compatible with join order hint.
	TiDBOptAdvancedJoinHint = "tidb_opt_advanced_join_hint"
	// TiDBOptUseInvisibleIndexes indicates whether to use invisible indexes.
	TiDBOptUseInvisibleIndexes = "tidb_opt_use_invisible_indexes"
	// TiDBAnalyzePartitionConcurrency is the number of concurrent workers to save statistics to the system tables.
	TiDBAnalyzePartitionConcurrency = "tidb_analyze_partition_concurrency"
	// TiDBMergePartitionStatsConcurrency indicates the concurrency when merge partition stats into global stats
	TiDBMergePartitionStatsConcurrency = "tidb_merge_partition_stats_concurrency"
	// TiDBEnableAsyncMergeGlobalStats indicates whether to enable async merge global stats
	TiDBEnableAsyncMergeGlobalStats = "tidb_enable_async_merge_global_stats"
	// TiDBOptPrefixIndexSingleScan indicates whether to do some optimizations to avoid double scan for prefix index.
	// When set to true, `col is (not) null`(`col` is index prefix column) is regarded as index filter rather than table filter.
	TiDBOptPrefixIndexSingleScan = "tidb_opt_prefix_index_single_scan"

	// TiDBEnableExternalTSRead indicates whether to enable read through an external ts
	TiDBEnableExternalTSRead = "tidb_enable_external_ts_read"

	// TiDBEnablePlanReplayerCapture indicates whether to enable plan replayer capture
	TiDBEnablePlanReplayerCapture = "tidb_enable_plan_replayer_capture"

	// TiDBEnablePlanReplayerContinuousCapture indicates whether to enable continuous capture
	TiDBEnablePlanReplayerContinuousCapture = "tidb_enable_plan_replayer_continuous_capture"
	// TiDBEnableReusechunk indicates whether to enable chunk alloc
	TiDBEnableReusechunk = "tidb_enable_reuse_chunk"

	// TiDBStoreBatchSize indicates the batch size of coprocessor in the same store.
	TiDBStoreBatchSize = "tidb_store_batch_size"

	// MppExchangeCompressionMode indicates the data compression method in mpp exchange operator
	MppExchangeCompressionMode = "mpp_exchange_compression_mode"

	// MppVersion indicates the mpp-version used to build mpp plan
	MppVersion = "mpp_version"

	// TiDBPessimisticTransactionFairLocking controls whether fair locking for pessimistic transaction
	// is enabled.
	TiDBPessimisticTransactionFairLocking = "tidb_pessimistic_txn_fair_locking"

	// TiDBEnablePlanCacheForParamLimit controls whether prepare statement with parameterized limit can be cached
	TiDBEnablePlanCacheForParamLimit = "tidb_enable_plan_cache_for_param_limit"

	// TiDBEnableINLJoinInnerMultiPattern indicates whether enable multi pattern for inner side of inl join
	TiDBEnableINLJoinInnerMultiPattern = "tidb_enable_inl_join_inner_multi_pattern"

	// TiFlashComputeDispatchPolicy indicates how to dispatch task to tiflash_compute nodes.
	TiFlashComputeDispatchPolicy = "tiflash_compute_dispatch_policy"

	// TiDBEnablePlanCacheForSubquery controls whether prepare statement with subquery can be cached
	TiDBEnablePlanCacheForSubquery = "tidb_enable_plan_cache_for_subquery"

	// TiDBOptEnableLateMaterialization indicates whether to enable late materialization
	TiDBOptEnableLateMaterialization = "tidb_opt_enable_late_materialization"
	// TiDBLoadBasedReplicaReadThreshold is the wait duration threshold to enable replica read automatically.
	TiDBLoadBasedReplicaReadThreshold = "tidb_load_based_replica_read_threshold"

	// TiDBOptOrderingIdxSelThresh is the threshold for optimizer to consider the ordering index.
	TiDBOptOrderingIdxSelThresh = "tidb_opt_ordering_index_selectivity_threshold"

	// TiDBOptOrderingIdxSelRatio is the ratio the optimizer will assume applies when non indexed filtering rows are found
	// via the ordering index.
	TiDBOptOrderingIdxSelRatio = "tidb_opt_ordering_index_selectivity_ratio"

	// TiDBOptEnableMPPSharedCTEExecution indicates whether the optimizer try to build shared CTE scan during MPP execution.
	TiDBOptEnableMPPSharedCTEExecution = "tidb_opt_enable_mpp_shared_cte_execution"
	// TiDBOptFixControl makes the user able to control some details of the optimizer behavior.
	TiDBOptFixControl = "tidb_opt_fix_control"

	// TiFlashReplicaRead is used to set the policy of TiFlash replica read when the query needs the TiFlash engine.
	TiFlashReplicaRead = "tiflash_replica_read"

	// TiDBLockUnchangedKeys indicates whether to lock duplicate keys in INSERT IGNORE and REPLACE statements,
	// or unchanged unique keys in UPDATE statements, see PR #42210 and #42713
	TiDBLockUnchangedKeys = "tidb_lock_unchanged_keys"

	// TiDBFastCheckTable enables fast check table.
	TiDBFastCheckTable = "tidb_enable_fast_table_check"

	// TiDBAnalyzeSkipColumnTypes indicates the column types whose statistics would not be collected when executing the ANALYZE command.
	TiDBAnalyzeSkipColumnTypes = "tidb_analyze_skip_column_types"

	// TiDBEnableCheckConstraint indicates whether to enable check constraint feature.
	TiDBEnableCheckConstraint = "tidb_enable_check_constraint"

	// TiDBOptEnableHashJoin indicates whether to enable hash join.
	TiDBOptEnableHashJoin = "tidb_opt_enable_hash_join"

	// TiDBHashJoinVersion indicates whether to use hash join implementation v2.
	TiDBHashJoinVersion = "tidb_hash_join_version"

	// TiDBOptIndexJoinBuild indicates which way to build index join.
	TiDBOptIndexJoinBuild = "tidb_opt_index_join_build_v2"

	// TiDBOptObjective indicates whether the optimizer should be more stable, predictable or more aggressive.
	// Please see comments of SessionVars.OptObjective for details.
	TiDBOptObjective = "tidb_opt_objective"

	// TiDBEnableParallelHashaggSpill is the name of the `tidb_enable_parallel_hashagg_spill` system variable
	TiDBEnableParallelHashaggSpill = "tidb_enable_parallel_hashagg_spill"

	// TiDBTxnEntrySizeLimit indicates the max size of a entry in membuf.
	TiDBTxnEntrySizeLimit = "tidb_txn_entry_size_limit"

	// TiDBSchemaCacheSize indicates the size of infoschema meta data which are cached in V2 implementation.
	TiDBSchemaCacheSize = "tidb_schema_cache_size"

	// DivPrecisionIncrement indicates the number of digits by which to increase the scale of the result of
	// division operations performed with the / operator.
	DivPrecisionIncrement = "div_precision_increment"

	// TiDBEnableSharedLockPromotion indicates whether the `select for share` statement would be executed
	// as `select for update` statements which do acquire pessimistic locks.
	TiDBEnableSharedLockPromotion = "tidb_enable_shared_lock_promotion"

	// TiDBAccelerateUserCreationUpdate decides whether tidb will load & update the whole user's data in-memory.
	TiDBAccelerateUserCreationUpdate = "tidb_accelerate_user_creation_update"
)

// TiDB vars that have only global scope
const (
	// TiDBGCEnable turns garbage collection on or OFF
	TiDBGCEnable = "tidb_gc_enable"
	// TiDBGCRunInterval sets the interval that GC runs
	TiDBGCRunInterval = "tidb_gc_run_interval"
	// TiDBGCLifetime sets the retention window of older versions
	TiDBGCLifetime = "tidb_gc_life_time"
	// TiDBGCConcurrency sets the concurrency of garbage collection. -1 = AUTO value
	TiDBGCConcurrency = "tidb_gc_concurrency"
	// TiDBGCScanLockMode enables the green GC feature (deprecated)
	TiDBGCScanLockMode = "tidb_gc_scan_lock_mode"
	// TiDBGCMaxWaitTime sets max time for gc advances the safepoint delayed by active transactions
	TiDBGCMaxWaitTime = "tidb_gc_max_wait_time"
	// TiDBEnableEnhancedSecurity restricts SUPER users from certain operations.
	TiDBEnableEnhancedSecurity = "tidb_enable_enhanced_security"
	// TiDBEnableHistoricalStats enables the historical statistics feature (default off)
	TiDBEnableHistoricalStats = "tidb_enable_historical_stats"
	// TiDBPersistAnalyzeOptions persists analyze options for later analyze and auto-analyze
	TiDBPersistAnalyzeOptions = "tidb_persist_analyze_options"
	// TiDBEnableColumnTracking enables collecting predicate columns.
	// DEPRECATED: This variable is deprecated, please do not use this variable.
	TiDBEnableColumnTracking = "tidb_enable_column_tracking"
	// TiDBAnalyzeColumnOptions specifies the default column selection strategy for both manual and automatic analyze operations.
	// It accepts two values:
	// `PREDICATE`: Analyze only the columns that are used in the predicates of the query.
	// `ALL`: Analyze all columns in the table.
	TiDBAnalyzeColumnOptions = "tidb_analyze_column_options"
	// TiDBDisableColumnTrackingTime records the last time TiDBEnableColumnTracking is set off.
	// It is used to invalidate the collected predicate columns after turning off TiDBEnableColumnTracking, which avoids physical deletion.
	// It doesn't have cache in memory, and we directly get/set the variable value from/to mysql.tidb.
	// DEPRECATED: This variable is deprecated, please do not use this variable.
	TiDBDisableColumnTrackingTime = "tidb_disable_column_tracking_time"
	// TiDBStatsLoadPseudoTimeout indicates whether to fallback to pseudo stats after load timeout.
	TiDBStatsLoadPseudoTimeout = "tidb_stats_load_pseudo_timeout"
	// TiDBMemQuotaBindingCache indicates the memory quota for the bind cache.
	TiDBMemQuotaBindingCache = "tidb_mem_quota_binding_cache"
	// TiDBRCReadCheckTS indicates the tso optimization for read-consistency read is enabled.
	TiDBRCReadCheckTS = "tidb_rc_read_check_ts"
	// TiDBRCWriteCheckTs indicates whether some special write statements don't get latest tso from PD at RC
	TiDBRCWriteCheckTs = "tidb_rc_write_check_ts"
	// TiDBCommitterConcurrency controls the number of running concurrent requests in the commit phase.
	TiDBCommitterConcurrency = "tidb_committer_concurrency"
	// TiDBPipelinedDmlResourcePolicy controls the number of running concurrent requests in the
	// pipelined flush action.
	TiDBPipelinedDmlResourcePolicy = "tidb_pipelined_dml_resource_policy"
	// TiDBEnableBatchDML enables batch dml.
	TiDBEnableBatchDML = "tidb_enable_batch_dml"
	// TiDBStatsCacheMemQuota records stats cache quota
	TiDBStatsCacheMemQuota = "tidb_stats_cache_mem_quota"
	// TiDBMemQuotaAnalyze indicates the memory quota for all analyze jobs.
	TiDBMemQuotaAnalyze = "tidb_mem_quota_analyze"
	// TiDBEnableAutoAnalyze determines whether TiDB executes automatic analysis.
	// In test, we disable it by default. See GlobalSystemVariableInitialValue for details.
	TiDBEnableAutoAnalyze = "tidb_enable_auto_analyze"
	// TiDBEnableAutoAnalyzePriorityQueue determines whether TiDB executes automatic analysis with priority queue.
	TiDBEnableAutoAnalyzePriorityQueue = "tidb_enable_auto_analyze_priority_queue"
	// TiDBMemOOMAction indicates what operation TiDB perform when a single SQL statement exceeds
	// the memory quota specified by tidb_mem_quota_query and cannot be spilled to disk.
	TiDBMemOOMAction = "tidb_mem_oom_action"
	// TiDBPrepPlanCacheMemoryGuardRatio is used to prevent [performance.max-memory] from being exceeded
	TiDBPrepPlanCacheMemoryGuardRatio = "tidb_prepared_plan_cache_memory_guard_ratio"
	// TiDBMaxAutoAnalyzeTime is the max time that auto analyze can run. If auto analyze runs longer than the value, it
	// will be killed. 0 indicates that there is no time limit.
	TiDBMaxAutoAnalyzeTime = "tidb_max_auto_analyze_time"
	// TiDBAutoAnalyzeConcurrency is the concurrency of the auto analyze
	TiDBAutoAnalyzeConcurrency = "tidb_auto_analyze_concurrency"
	// TiDBEnableDistTask indicates whether to enable the distributed execute background tasks(For example DDL, Import etc).
	TiDBEnableDistTask = "tidb_enable_dist_task"
	// TiDBMaxDistTaskNodes indicates the max node count that could be used by distributed execution framework.
	TiDBMaxDistTaskNodes = "tidb_max_dist_task_nodes"
	// TiDBEnableFastCreateTable indicates whether to enable the fast create table feature.
	TiDBEnableFastCreateTable = "tidb_enable_fast_create_table"
	// TiDBGenerateBinaryPlan indicates whether binary plan should be generated in slow log and statements summary.
	TiDBGenerateBinaryPlan = "tidb_generate_binary_plan"
	// TiDBEnableGCAwareMemoryTrack indicates whether to turn-on GC-aware memory track.
	TiDBEnableGCAwareMemoryTrack = "tidb_enable_gc_aware_memory_track"
	// TiDBEnableTmpStorageOnOOM controls whether to enable the temporary storage for some operators
	// when a single SQL statement exceeds the memory quota specified by the memory quota.
	TiDBEnableTmpStorageOnOOM = "tidb_enable_tmp_storage_on_oom"
	// TiDBDDLEnableFastReorg indicates whether to use lighting backfill process for adding index.
	TiDBDDLEnableFastReorg = "tidb_ddl_enable_fast_reorg"
	// TiDBDDLDiskQuota used to set disk quota for lightning add index.
	TiDBDDLDiskQuota = "tidb_ddl_disk_quota"
	// TiDBCloudStorageURI used to set a cloud storage uri for ddl add index and import into.
	TiDBCloudStorageURI = "tidb_cloud_storage_uri"
	// TiDBAutoBuildStatsConcurrency is the number of concurrent workers to automatically analyze tables or partitions.
	// It is very similar to the `tidb_build_stats_concurrency` variable, but it is used for the auto analyze feature.
	TiDBAutoBuildStatsConcurrency = "tidb_auto_build_stats_concurrency"
	// TiDBSysProcScanConcurrency is used to set the scan concurrency of for backend system processes, like auto-analyze.
	// For now, it controls the number of concurrent workers to scan regions to collect statistics (FMSketch, Samples).
	TiDBSysProcScanConcurrency = "tidb_sysproc_scan_concurrency"
	// TiDBServerMemoryLimit indicates the memory limit of the tidb-server instance.
	TiDBServerMemoryLimit = "tidb_server_memory_limit"
	// TiDBServerMemoryLimitSessMinSize indicates the minimal memory used of a session, that becomes a candidate for session kill.
	TiDBServerMemoryLimitSessMinSize = "tidb_server_memory_limit_sess_min_size"
	// TiDBServerMemoryLimitGCTrigger indicates the gc percentage of the TiDBServerMemoryLimit.
	TiDBServerMemoryLimitGCTrigger = "tidb_server_memory_limit_gc_trigger"
	// TiDBEnableGOGCTuner is to enable GOGC tuner. it can tuner GOGC
	TiDBEnableGOGCTuner = "tidb_enable_gogc_tuner"
	// TiDBGOGCTunerThreshold is to control the threshold of GOGC tuner.
	TiDBGOGCTunerThreshold = "tidb_gogc_tuner_threshold"
	// TiDBGOGCTunerMaxValue is the max value of GOGC that GOGC tuner can change to.
	TiDBGOGCTunerMaxValue = "tidb_gogc_tuner_max_value"
	// TiDBGOGCTunerMinValue is the min value of GOGC that GOGC tuner can change to.
	TiDBGOGCTunerMinValue = "tidb_gogc_tuner_min_value"
	// TiDBExternalTS is the ts to read through when the `TiDBEnableExternalTsRead` is on
	TiDBExternalTS = "tidb_external_ts"
	// TiDBTTLJobEnable is used to enable/disable scheduling ttl job
	TiDBTTLJobEnable = "tidb_ttl_job_enable"
	// TiDBTTLScanBatchSize is used to control the batch size in the SELECT statement for TTL jobs
	TiDBTTLScanBatchSize = "tidb_ttl_scan_batch_size"
	// TiDBTTLDeleteBatchSize is used to control the batch size in the DELETE statement for TTL jobs
	TiDBTTLDeleteBatchSize = "tidb_ttl_delete_batch_size"
	// TiDBTTLDeleteRateLimit is used to control the delete rate limit for TTL jobs in each node
	TiDBTTLDeleteRateLimit = "tidb_ttl_delete_rate_limit"
	// TiDBTTLJobScheduleWindowStartTime is used to restrict the start time of the time window of scheduling the ttl jobs.
	TiDBTTLJobScheduleWindowStartTime = "tidb_ttl_job_schedule_window_start_time"
	// TiDBTTLJobScheduleWindowEndTime is used to restrict the end time of the time window of scheduling the ttl jobs.
	TiDBTTLJobScheduleWindowEndTime = "tidb_ttl_job_schedule_window_end_time"
	// TiDBTTLScanWorkerCount indicates the count of the scan workers in each TiDB node
	TiDBTTLScanWorkerCount = "tidb_ttl_scan_worker_count"
	// TiDBTTLDeleteWorkerCount indicates the count of the delete workers in each TiDB node
	TiDBTTLDeleteWorkerCount = "tidb_ttl_delete_worker_count"
	// PasswordReuseHistory limit a few passwords to reuse.
	PasswordReuseHistory = "password_history"
	// PasswordReuseTime limit how long passwords can be reused.
	PasswordReuseTime = "password_reuse_interval"
	// TiDBHistoricalStatsDuration indicates the duration to remain tidb historical stats
	TiDBHistoricalStatsDuration = "tidb_historical_stats_duration"
	// TiDBEnableHistoricalStatsForCapture indicates whether use historical stats in plan replayer capture
	TiDBEnableHistoricalStatsForCapture = "tidb_enable_historical_stats_for_capture"
	// TiDBEnableResourceControl indicates whether resource control feature is enabled
	TiDBEnableResourceControl = "tidb_enable_resource_control"
	// TiDBResourceControlStrictMode indicates whether resource control strict mode is enabled.
	// When strict mode is enabled, user need certain privilege to change session or statement resource group.
	TiDBResourceControlStrictMode = "tidb_resource_control_strict_mode"
	// TiDBStmtSummaryEnablePersistent indicates whether to enable file persistence for stmtsummary.
	TiDBStmtSummaryEnablePersistent = "tidb_stmt_summary_enable_persistent"
	// TiDBStmtSummaryFilename indicates the file name written by stmtsummary.
	TiDBStmtSummaryFilename = "tidb_stmt_summary_filename"
	// TiDBStmtSummaryFileMaxDays indicates how many days the files written by stmtsummary will be kept.
	TiDBStmtSummaryFileMaxDays = "tidb_stmt_summary_file_max_days"
	// TiDBStmtSummaryFileMaxSize indicates the maximum size (in mb) of a single file written by stmtsummary.
	TiDBStmtSummaryFileMaxSize = "tidb_stmt_summary_file_max_size"
	// TiDBStmtSummaryFileMaxBackups indicates the maximum number of files written by stmtsummary.
	TiDBStmtSummaryFileMaxBackups = "tidb_stmt_summary_file_max_backups"
	// TiDBTTLRunningTasks limits the count of running ttl tasks. Default to 0, means 3 times the count of TiKV (or no
	// limitation, if the storage is not TiKV).
	TiDBTTLRunningTasks = "tidb_ttl_running_tasks"
	// AuthenticationLDAPSASLAuthMethodName defines the authentication method used by LDAP SASL authentication plugin
	AuthenticationLDAPSASLAuthMethodName = "authentication_ldap_sasl_auth_method_name"
	// AuthenticationLDAPSASLCAPath defines the ca certificate to verify LDAP connection in LDAP SASL authentication plugin
	AuthenticationLDAPSASLCAPath = "authentication_ldap_sasl_ca_path"
	// AuthenticationLDAPSASLTLS defines whether to use TLS connection in LDAP SASL authentication plugin
	AuthenticationLDAPSASLTLS = "authentication_ldap_sasl_tls"
	// AuthenticationLDAPSASLServerHost defines the server host of LDAP server for LDAP SASL authentication plugin
	AuthenticationLDAPSASLServerHost = "authentication_ldap_sasl_server_host"
	// AuthenticationLDAPSASLServerPort defines the port of LDAP server for LDAP SASL authentication plugin
	AuthenticationLDAPSASLServerPort = "authentication_ldap_sasl_server_port"
	// AuthenticationLDAPSASLReferral defines whether to enable LDAP referral for LDAP SASL authentication plugin
	AuthenticationLDAPSASLReferral = "authentication_ldap_sasl_referral"
	// AuthenticationLDAPSASLUserSearchAttr defines the attribute of username in LDAP server
	AuthenticationLDAPSASLUserSearchAttr = "authentication_ldap_sasl_user_search_attr"
	// AuthenticationLDAPSASLBindBaseDN defines the `dn` to search the users in. It's used to limit the search scope of TiDB.
	AuthenticationLDAPSASLBindBaseDN = "authentication_ldap_sasl_bind_base_dn"
	// AuthenticationLDAPSASLBindRootDN defines the `dn` of the user to login the LDAP server and perform search.
	AuthenticationLDAPSASLBindRootDN = "authentication_ldap_sasl_bind_root_dn"
	// AuthenticationLDAPSASLBindRootPWD defines the password of the user to login the LDAP server and perform search.
	AuthenticationLDAPSASLBindRootPWD = "authentication_ldap_sasl_bind_root_pwd"
	// AuthenticationLDAPSASLInitPoolSize defines the init size of connection pool to LDAP server for SASL plugin.
	AuthenticationLDAPSASLInitPoolSize = "authentication_ldap_sasl_init_pool_size"
	// AuthenticationLDAPSASLMaxPoolSize defines the max size of connection pool to LDAP server for SASL plugin.
	AuthenticationLDAPSASLMaxPoolSize = "authentication_ldap_sasl_max_pool_size"
	// AuthenticationLDAPSimpleAuthMethodName defines the authentication method used by LDAP Simple authentication plugin
	AuthenticationLDAPSimpleAuthMethodName = "authentication_ldap_simple_auth_method_name"
	// AuthenticationLDAPSimpleCAPath defines the ca certificate to verify LDAP connection in LDAP Simple authentication plugin
	AuthenticationLDAPSimpleCAPath = "authentication_ldap_simple_ca_path"
	// AuthenticationLDAPSimpleTLS defines whether to use TLS connection in LDAP Simple authentication plugin
	AuthenticationLDAPSimpleTLS = "authentication_ldap_simple_tls"
	// AuthenticationLDAPSimpleServerHost defines the server host of LDAP server for LDAP Simple authentication plugin
	AuthenticationLDAPSimpleServerHost = "authentication_ldap_simple_server_host"
	// AuthenticationLDAPSimpleServerPort defines the port of LDAP server for LDAP Simple authentication plugin
	AuthenticationLDAPSimpleServerPort = "authentication_ldap_simple_server_port"
	// AuthenticationLDAPSimpleReferral defines whether to enable LDAP referral for LDAP Simple authentication plugin
	AuthenticationLDAPSimpleReferral = "authentication_ldap_simple_referral"
	// AuthenticationLDAPSimpleUserSearchAttr defines the attribute of username in LDAP server
	AuthenticationLDAPSimpleUserSearchAttr = "authentication_ldap_simple_user_search_attr"
	// AuthenticationLDAPSimpleBindBaseDN defines the `dn` to search the users in. It's used to limit the search scope of TiDB.
	AuthenticationLDAPSimpleBindBaseDN = "authentication_ldap_simple_bind_base_dn"
	// AuthenticationLDAPSimpleBindRootDN defines the `dn` of the user to login the LDAP server and perform search.
	AuthenticationLDAPSimpleBindRootDN = "authentication_ldap_simple_bind_root_dn"
	// AuthenticationLDAPSimpleBindRootPWD defines the password of the user to login the LDAP server and perform search.
	AuthenticationLDAPSimpleBindRootPWD = "authentication_ldap_simple_bind_root_pwd"
	// AuthenticationLDAPSimpleInitPoolSize defines the init size of connection pool to LDAP server for SASL plugin.
	AuthenticationLDAPSimpleInitPoolSize = "authentication_ldap_simple_init_pool_size"
	// AuthenticationLDAPSimpleMaxPoolSize defines the max size of connection pool to LDAP server for SASL plugin.
	AuthenticationLDAPSimpleMaxPoolSize = "authentication_ldap_simple_max_pool_size"
	// TiDBRuntimeFilterTypeName the value of is string, a runtime filter type list split by ",", such as: "IN,MIN_MAX"
	TiDBRuntimeFilterTypeName = "tidb_runtime_filter_type"
	// TiDBRuntimeFilterModeName the mode of runtime filter, such as "OFF", "LOCAL"
	TiDBRuntimeFilterModeName = "tidb_runtime_filter_mode"
	// TiDBSkipMissingPartitionStats controls how to handle missing partition stats when merging partition stats to global stats.
	// When set to true, skip missing partition stats and continue to merge other partition stats to global stats.
	// When set to false, give up merging partition stats to global stats.
	TiDBSkipMissingPartitionStats = "tidb_skip_missing_partition_stats"
	// TiDBSessionAlias indicates the alias of a session which is used for tracing.
	TiDBSessionAlias = "tidb_session_alias"
	// TiDBServiceScope indicates the role for tidb for distributed task framework.
	TiDBServiceScope = "tidb_service_scope"
	// TiDBSchemaVersionCacheLimit defines the capacity size of domain infoSchema cache.
	TiDBSchemaVersionCacheLimit = "tidb_schema_version_cache_limit"
	// TiDBEnableTiFlashPipelineMode means if we should use pipeline model to execute query or not in tiflash.
	// It's deprecated and setting it will not have any effect.
	TiDBEnableTiFlashPipelineMode = "tidb_enable_tiflash_pipeline_model"
	// TiDBIdleTransactionTimeout indicates the maximum time duration a transaction could be idle, unit is second.
	// Any idle transaction will be killed after being idle for `tidb_idle_transaction_timeout` seconds.
	// This is similar to https://docs.percona.com/percona-server/5.7/management/innodb_kill_idle_trx.html and https://mariadb.com/kb/en/transaction-timeouts/
	TiDBIdleTransactionTimeout = "tidb_idle_transaction_timeout"
	// TiDBLowResolutionTSOUpdateInterval defines how often to refresh low resolution timestamps.
	TiDBLowResolutionTSOUpdateInterval = "tidb_low_resolution_tso_update_interval"
	// TiDBDMLType indicates the execution type of DML in TiDB.
	// The value can be STANDARD, BULK.
	// Currently, the BULK mode only affects auto-committed DML.
	TiDBDMLType = "tidb_dml_type"
	// TiFlashHashAggPreAggMode indicates the policy of 1st hashagg.
	TiFlashHashAggPreAggMode = "tiflash_hashagg_preaggregation_mode"
	// TiDBEnableLazyCursorFetch defines whether to enable the lazy cursor fetch. If it's `OFF`, all results of
	// of a cursor will be stored in the tidb node in `EXECUTE` command.
	TiDBEnableLazyCursorFetch = "tidb_enable_lazy_cursor_fetch"
	// TiDBTSOClientRPCMode controls how the TSO client performs the TSO RPC requests. It internally controls the
	// concurrency of the RPC. This variable provides an approach to tune the latency of getting timestamps from PD.
	TiDBTSOClientRPCMode = "tidb_tso_client_rpc_mode"
	// TiDBCircuitBreakerPDMetadataErrorRateThresholdRatio variable is used to set ratio of errors to trip the circuit breaker for get region calls to PD
	// https://github.com/tikv/rfcs/blob/master/text/0115-circuit-breaker.md
	TiDBCircuitBreakerPDMetadataErrorRateThresholdRatio = "tidb_cb_pd_metadata_error_rate_threshold_ratio"

	// TiDBEnableTSValidation controls whether to enable the timestamp validation in client-go.
	TiDBEnableTSValidation = "tidb_enable_ts_validation"
)

// TiDB intentional limits, can be raised in the future.
const (
	// MaxConfigurableConcurrency is the maximum number of "threads" (goroutines) that can be specified
	// for any type of configuration item that has concurrent workers.
	MaxConfigurableConcurrency = 256

	// MaxShardRowIDBits is the maximum number of bits that can be used for row-id sharding.
	MaxShardRowIDBits = 15

	// MaxPreSplitRegions is the maximum number of regions that can be pre-split.
	MaxPreSplitRegions = 15
)

// Pipelined-DML related constants
const (
	// MinPipelinedDMLConcurrency is the minimum acceptable concurrency
	MinPipelinedDMLConcurrency = 1
	// MaxPipelinedDMLConcurrency is the maximum acceptable concurrency
	MaxPipelinedDMLConcurrency = 8192

	// DefaultFlushConcurrency is the default flush concurrency
	DefaultFlushConcurrency = 128
	// DefaultResolveConcurrency is the default resolve_lock concurrency
	DefaultResolveConcurrency = 8

	// ConservativeFlushConcurrency is the flush concurrency in conservative mode
	ConservativeFlushConcurrency = 2
	// ConservativeResolveConcurrency is the resolve_lock concurrency in conservative mode
	ConservativeResolveConcurrency = 2
)

// Default TiDB system variable values.
const (
	DefHostname                             = "localhost"
	DefIndexLookupConcurrency               = ConcurrencyUnset
	DefIndexLookupJoinConcurrency           = ConcurrencyUnset
	DefIndexSerialScanConcurrency           = 1
	DefIndexJoinBatchSize                   = 25000
	DefIndexLookupSize                      = 20000
	DefDistSQLScanConcurrency               = 15
	DefAnalyzeDistSQLScanConcurrency        = 4
	DefBuildStatsConcurrency                = 2
	DefBuildSamplingStatsConcurrency        = 2
	DefAutoAnalyzeRatio                     = 0.5
	DefAutoAnalyzeStartTime                 = "00:00 +0000"
	DefAutoAnalyzeEndTime                   = "23:59 +0000"
	DefAutoIncrementIncrement               = 1
	DefAutoIncrementOffset                  = 1
	DefChecksumTableConcurrency             = 4
	DefSkipUTF8Check                        = false
	DefSkipASCIICheck                       = false
	DefOptAggPushDown                       = false
	DefOptDeriveTopN                        = false
	DefOptCartesianBCJ                      = 1
	DefOptMPPOuterJoinFixedBuildSide        = false
	DefOptWriteRowID                        = false
	DefOptEnableCorrelationAdjustment       = true
	DefOptLimitPushDownThreshold            = 100
	DefOptCorrelationThreshold              = 0.9
	DefOptCorrelationExpFactor              = 1
	DefOptRiskEqSkewRatio                   = 0.0
	DefOptRiskRangeSkewRatio                = 0.0
	DefOptCPUFactor                         = 3.0
	DefOptCopCPUFactor                      = 3.0
	DefOptTiFlashConcurrencyFactor          = 24.0
	DefOptNetworkFactor                     = 1.0
	DefOptScanFactor                        = 1.5
	DefOptDescScanFactor                    = 3.0
	DefOptSeekFactor                        = 20.0
	DefOptMemoryFactor                      = 0.001
	DefOptDiskFactor                        = 1.5
	DefOptConcurrencyFactor                 = 3.0
	DefOptIndexScanCostFactor               = 1.0
	DefOptIndexReaderCostFactor             = 1.0
	DefOptTableReaderCostFactor             = 1.0
	DefOptTableFullScanCostFactor           = 1.0
	DefOptTableRangeScanCostFactor          = 1.0
	DefOptTableRowIDScanCostFactor          = 1.0
	DefOptTableTiFlashScanCostFactor        = 1.0
	DefOptIndexLookupCostFactor             = 1.0
	DefOptIndexMergeCostFactor              = 1.0
	DefOptSortCostFactor                    = 1.0
	DefOptTopNCostFactor                    = 1.0
	DefOptLimitCostFactor                   = 1.0
	DefOptStreamAggCostFactor               = 1.0
	DefOptHashAggCostFactor                 = 1.0
	DefOptMergeJoinCostFactor               = 1.0
	DefOptHashJoinCostFactor                = 1.0
	DefOptIndexJoinCostFactor               = 1.0
	DefOptForceInlineCTE                    = false
	DefOptInSubqToJoinAndAgg                = true
	DefOptPreferRangeScan                   = true
	DefBatchInsert                          = false
	DefBatchDelete                          = false
	DefBatchCommit                          = false
	DefCurretTS                             = 0
	DefInitChunkSize                        = 32
	DefMinPagingSize                        = int(paging.MinPagingSize)
	DefMaxPagingSize                        = int(paging.MaxPagingSize)
	DefMaxChunkSize                         = 1024
	DefDMLBatchSize                         = 0
	DefMaxPreparedStmtCount                 = -1
	DefWaitTimeout                          = 28800
	DefTiDBMemQuotaApplyCache               = 32 << 20 // 32MB.
	DefTiDBMemQuotaBindingCache             = 64 << 20 // 64MB.
	DefTiDBGeneralLog                       = false
	DefTiDBPProfSQLCPU                      = 0
	DefTiDBRetryLimit                       = 10
	DefTiDBDisableTxnAutoRetry              = true
	DefTiDBConstraintCheckInPlace           = false
	DefTiDBHashJoinConcurrency              = ConcurrencyUnset
	DefTiDBProjectionConcurrency            = ConcurrencyUnset
	DefBroadcastJoinThresholdSize           = 100 * 1024 * 1024
	DefBroadcastJoinThresholdCount          = 10 * 1024
	DefPreferBCJByExchangeDataSize          = false
	DefTiDBOptimizerSelectivityLevel        = 0
	DefTiDBOptimizerEnableNewOFGB           = false
	DefTiDBEnableOuterJoinReorder           = true
	DefTiDBEnableNAAJ                       = true
	DefTiDBAllowBatchCop                    = 1
	DefShardRowIDBits                       = 0
	DefPreSplitRegions                      = 0
	DefBlockEncryptionMode                  = "aes-128-ecb"
	DefTiDBAllowMPPExecution                = true
	DefTiDBAllowTiFlashCop                  = false
	DefTiDBHashExchangeWithNewCollation     = true
	DefTiDBEnforceMPPExecution              = false
	DefTiFlashMaxThreads                    = -1
	DefTiFlashMaxBytesBeforeExternalJoin    = -1
	DefTiFlashMaxBytesBeforeExternalGroupBy = -1
	DefTiFlashMaxBytesBeforeExternalSort    = -1
	DefTiFlashMemQuotaQueryPerNode          = 0
	DefTiFlashQuerySpillRatio               = 0.7
	DefTiFlashHashJoinVersion               = joinversion.TiFlashHashJoinVersionDefVal
	DefTiDBEnableTiFlashPipelineMode        = true
	DefTiDBMPPStoreFailTTL                  = "0s"
	DefTiDBTxnMode                          = PessimisticTxnMode
	DefTiDBRowFormatV1                      = 1
	DefTiDBRowFormatV2                      = 2
	DefTiDBDDLReorgWorkerCount              = 4
	DefTiDBDDLReorgBatchSize                = 256
	DefTiDBDDLFlashbackConcurrency          = 64
	DefTiDBDDLErrorCountLimit               = 512
	DefTiDBDDLReorgMaxWriteSpeed            = 0
	DefTiDBMaxDeltaSchemaCount              = 1024
	DefTiDBPlacementMode                    = PlacementModeStrict
	DefTiDBEnableAutoIncrementInGenerated   = false
	DefTiDBHashAggPartialConcurrency        = ConcurrencyUnset
	DefTiDBHashAggFinalConcurrency          = ConcurrencyUnset
	DefTiDBWindowConcurrency                = ConcurrencyUnset
	DefTiDBMergeJoinConcurrency             = 1 // disable optimization by default
	DefTiDBStreamAggConcurrency             = 1
	DefTiDBForcePriority                    = mysql.NoPriority
	DefEnableWindowFunction                 = true
	DefEnablePipelinedWindowFunction        = true
	DefEnableStrictDoubleTypeCheck          = true
	DefEnableVectorizedExpression           = true
	DefTiDBOptJoinReorderThreshold          = 0
	DefTiDBDDLSlowOprThreshold              = 300
	DefTiDBUseFastAnalyze                   = false
	DefTiDBSkipIsolationLevelCheck          = false
	DefTiDBExpensiveQueryTimeThreshold      = 60      // 60s
	DefTiDBExpensiveTxnTimeThreshold        = 60 * 10 // 10 minutes
	DefTiDBScatterRegion                    = ScatterOff
	DefTiDBWaitSplitRegionFinish            = true
	DefWaitSplitRegionTimeout               = 300 // 300s
	DefTiDBEnableNoopFuncs                  = Off
	DefTiDBEnableNoopVariables              = true
	DefTiDBAllowRemoveAutoInc               = false
	DefTiDBUsePlanBaselines                 = true
	DefTiDBEvolvePlanBaselines              = false
	DefTiDBEvolvePlanTaskMaxTime            = 600 // 600s
	DefTiDBEvolvePlanTaskStartTime          = "00:00 +0000"
	DefTiDBEvolvePlanTaskEndTime            = "23:59 +0000"
	DefInnodbLockWaitTimeout                = 50 // 50s
	DefTiDBStoreLimit                       = 0
	DefTiDBMetricSchemaStep                 = 60 // 60s
	DefTiDBMetricSchemaRangeDuration        = 60 // 60s
	DefTiDBFoundInPlanCache                 = false
	DefTiDBFoundInBinding                   = false
	DefTiDBEnableCollectExecutionInfo       = true
	DefTiDBAllowAutoRandExplicitInsert      = false
	DefTiDBEnableClusteredIndex             = ClusteredIndexDefModeOn
	DefTiDBRedactLog                        = Off
	DefTiDBRestrictedReadOnly               = false
	DefTiDBSuperReadOnly                    = false
	DefTiDBShardAllocateStep                = math.MaxInt64
	DefTiDBPointGetCache                    = false
	DefTiDBEnableTelemetry                  = true
	DefTiDBEnableParallelApply              = false
	DefTiDBPartitionPruneMode               = "dynamic"
	DefTiDBEnableRateLimitAction            = false
	DefTiDBEnableAsyncCommit                = false
	DefTiDBEnable1PC                        = false
	DefTiDBGuaranteeLinearizability         = true
	DefTiDBAnalyzeVersion                   = 2
	// Deprecated: This variable is deprecated, please do not use this variable.
	DefTiDBAutoAnalyzePartitionBatchSize              = mysql.PartitionCountLimit
	DefTiDBEnableIndexMergeJoin                       = false
	DefTiDBTrackAggregateMemoryUsage                  = true
	DefCTEMaxRecursionDepth                           = 1000
	DefTiDBTmpTableMaxSize                            = 64 << 20 // 64MB.
	DefTiDBEnableLocalTxn                             = false
	DefTiDBTSOClientBatchMaxWaitTime                  = 0.0 // 0ms
	DefTiDBEnableTSOFollowerProxy                     = false
	DefPDEnableFollowerHandleRegion                   = true
	DefTiDBEnableBatchQueryRegion                     = false
	DefTiDBEnableOrderedResultMode                    = false
	DefTiDBEnablePseudoForOutdatedStats               = false
	DefTiDBRegardNULLAsPoint                          = true
	DefEnablePlacementCheck                           = true
	DefTimestamp                                      = "0"
	DefTimestampFloat                                 = 0.0
	DefTiDBEnableStmtSummary                          = true
	DefTiDBStmtSummaryInternalQuery                   = false
	DefTiDBStmtSummaryRefreshInterval                 = 1800
	DefTiDBStmtSummaryHistorySize                     = 24
	DefTiDBStmtSummaryMaxStmtCount                    = 3000
	DefTiDBStmtSummaryMaxSQLLength                    = 4096
	DefTiDBCapturePlanBaseline                        = Off
	DefTiDBIgnoreInlistPlanDigest                     = false
	DefTiDBEnableIndexMerge                           = true
	DefEnableLegacyInstanceScope                      = true
	DefTiDBTableCacheLease                            = 3 // 3s
	DefTiDBPersistAnalyzeOptions                      = true
	DefTiDBStatsLoadSyncWait                          = 100
	DefTiDBStatsLoadPseudoTimeout                     = true
	DefSysdateIsNow                                   = false
	DefTiDBEnableParallelHashaggSpill                 = true
	DefTiDBEnableMutationChecker                      = false
	DefTiDBTxnAssertionLevel                          = AssertionOffStr
	DefTiDBIgnorePreparedCacheCloseStmt               = false
	DefTiDBBatchPendingTiFlashCount                   = 4000
	DefRCReadCheckTS                                  = false
	DefTiDBRemoveOrderbyInSubquery                    = true
	DefTiDBSkewDistinctAgg                            = false
	DefTiDB3StageDistinctAgg                          = true
	DefTiDB3StageMultiDistinctAgg                     = false
	DefTiDBOptExplainEvaledSubquery                   = false
	DefTiDBReadStaleness                              = 0
	DefTiDBGCMaxWaitTime                              = 24 * 60 * 60
	DefMaxAllowedPacket                        uint64 = 67108864
	DefTiDBEnableBatchDML                             = false
	DefTiDBMemQuotaQuery                              = memory.DefMemQuotaQuery // 1GB
	DefTiDBStatsCacheMemQuota                         = 0
	MaxTiDBStatsCacheMemQuota                         = 1024 * 1024 * 1024 * 1024 // 1TB
	DefTiDBQueryLogMaxLen                             = 4096
	DefRequireSecureTransport                         = false
	DefTiDBCommitterConcurrency                       = 128
	DefTiDBPipelinedDmlResourcePolicy                 = StrategyStandard
	DefTiDBBatchDMLIgnoreError                        = false
	DefTiDBMemQuotaAnalyze                            = -1
	DefTiDBEnableAutoAnalyze                          = true
	DefTiDBEnableAutoAnalyzePriorityQueue             = true
	DefTiDBAnalyzeColumnOptions                       = "PREDICATE"
	DefTiDBMemOOMAction                               = "CANCEL"
	DefTiDBMaxAutoAnalyzeTime                         = 12 * 60 * 60
	DefTiDBAutoAnalyzeConcurrency                     = 1
	DefTiDBEnablePrepPlanCache                        = true
	DefTiDBPrepPlanCacheSize                          = 100
	DefTiDBSessionPlanCacheSize                       = 100
	DefTiDBEnablePrepPlanCacheMemoryMonitor           = true
	DefTiDBPrepPlanCacheMemoryGuardRatio              = 0.1
	DefTiDBEnableWorkloadBasedLearning                = false
	DefTiDBWorkloadBasedLearningInterval              = 24 * time.Hour
	DefTiDBEnableDistTask                             = true
	DefTiDBMaxDistTaskNodes                           = -1
	DefTiDBEnableFastCreateTable                      = true
	DefTiDBSimplifiedMetrics                          = false
	DefTiDBEnablePaging                               = true
	DefTiFlashFineGrainedShuffleStreamCount           = 0
	DefStreamCountWhenMaxThreadsNotSet                = 8
	DefTiFlashFineGrainedShuffleBatchSize             = 8192
	DefAdaptiveClosestReadThreshold                   = 4096
	DefTiDBEnableAnalyzeSnapshot                      = false
	DefTiDBGenerateBinaryPlan                         = true
	DefEnableTiDBGCAwareMemoryTrack                   = false
	DefTiDBDefaultStrMatchSelectivity                 = 0.8
	DefTiDBEnableTmpStorageOnOOM                      = true
	DefTiDBEnableMDL                                  = true
	DefTiFlashFastScan                                = false
	DefMemoryUsageAlarmRatio                          = 0.7
	DefMemoryUsageAlarmKeepRecordNum                  = 5
	DefTiDBEnableFastReorg                            = true
	DefTiDBDDLDiskQuota                               = 100 * 1024 * 1024 * 1024 // 100GB
	DefExecutorConcurrency                            = 5
	DefTiDBEnableNonPreparedPlanCache                 = false
	DefTiDBEnableNonPreparedPlanCacheForDML           = true
	DefTiDBNonPreparedPlanCacheSize                   = 100
	DefTiDBPlanCacheMaxPlanSize                       = 2 * size.MB
	DefTiDBInstancePlanCacheMaxMemSize                = 100 * size.MB
	MinTiDBInstancePlanCacheMemSize                   = 100 * size.MB
	DefTiDBInstancePlanCacheReservedPercentage        = 0.1
	// MaxDDLReorgBatchSize is exported for testing.
	MaxDDLReorgBatchSize                  int32  = 10240
	MinDDLReorgBatchSize                  int32  = 32
	MinExpensiveQueryTimeThreshold        uint64 = 10 // 10s
	MinExpensiveTxnTimeThreshold          uint64 = 60 // 60s
	DefTiDBAutoBuildStatsConcurrency             = 1
	DefTiDBSysProcScanConcurrency                = 1
	DefTiDBRcWriteCheckTs                        = false
	DefTiDBForeignKeyChecks                      = true
	DefTiDBOptAdvancedJoinHint                   = true
	DefTiDBAnalyzePartitionConcurrency           = 2
	DefTiDBOptRangeMaxSize                       = 64 * int64(size.MB) // 64 MB
	DefTiDBCostModelVer                          = 2
	DefTiDBServerMemoryLimitSessMinSize          = 128 << 20
	DefTiDBMergePartitionStatsConcurrency        = 1
	DefTiDBServerMemoryLimitGCTrigger            = 0.7
	DefTiDBEnableGOGCTuner                       = true
	// DefTiDBGOGCTunerThreshold is to limit TiDBGOGCTunerThreshold.
	DefTiDBGOGCTunerThreshold                 float64 = 0.6
	DefTiDBGOGCMaxValue                               = 500
	DefTiDBGOGCMinValue                               = 100
	DefTiDBOptPrefixIndexSingleScan                   = true
	DefTiDBEnableAsyncMergeGlobalStats                = true
	DefTiDBExternalTS                                 = 0
	DefTiDBEnableExternalTSRead                       = false
	DefTiDBEnableReusechunk                           = true
	DefTiDBUseAlloc                                   = false
	DefTiDBEnablePlanReplayerCapture                  = true
	DefTiDBIndexMergeIntersectionConcurrency          = ConcurrencyUnset
	DefTiDBTTLJobEnable                               = true
	DefTiDBTTLScanBatchSize                           = 500
	DefTiDBTTLScanBatchMaxSize                        = 10240
	DefTiDBTTLScanBatchMinSize                        = 1
	DefTiDBTTLDeleteBatchSize                         = 100
	DefTiDBTTLDeleteBatchMaxSize                      = 10240
	DefTiDBTTLDeleteBatchMinSize                      = 1
	DefTiDBTTLDeleteRateLimit                         = 0
	DefTiDBTTLRunningTasks                            = -1
	DefPasswordReuseHistory                           = 0
	DefPasswordReuseTime                              = 0
	DefMaxUserConnections                             = 0
	DefTiDBStoreBatchSize                             = 4
	DefTiDBHistoricalStatsDuration                    = 7 * 24 * time.Hour
	DefTiDBEnableHistoricalStatsForCapture            = false
	DefTiDBTTLJobScheduleWindowStartTime              = "00:00 +0000"
	DefTiDBTTLJobScheduleWindowEndTime                = "23:59 +0000"
	DefTiDBTTLScanWorkerCount                         = 4
	DefTiDBTTLDeleteWorkerCount                       = 4
	DefaultExchangeCompressionMode                    = ExchangeCompressionModeUnspecified
	DefTiDBEnableResourceControl                      = true
	DefTiDBResourceControlStrictMode                  = true
	DefTiDBPessimisticTransactionFairLocking          = false
	DefTiDBEnablePlanCacheForParamLimit               = true
	DefTiDBEnableINLJoinMultiPattern                  = true
	DefTiFlashComputeDispatchPolicy                   = DispatchPolicyConsistentHashStr
	DefTiDBEnablePlanCacheForSubquery                 = true
	DefTiDBLoadBasedReplicaReadThreshold              = time.Second
	DefTiDBOptEnableLateMaterialization               = true
	DefTiDBOptOrderingIdxSelThresh                    = 0.0
	DefTiDBOptOrderingIdxSelRatio                     = 0.01
	DefTiDBOptEnableMPPSharedCTEExecution             = false
	DefTiDBPlanCacheInvalidationOnFreshStats          = true
	DefTiDBEnableRowLevelChecksum                     = false
	DefAuthenticationLDAPSASLAuthMethodName           = "SCRAM-SHA-1"
	DefAuthenticationLDAPSASLServerPort               = 389
	DefAuthenticationLDAPSASLTLS                      = false
	DefAuthenticationLDAPSASLUserSearchAttr           = "uid"
	DefAuthenticationLDAPSASLInitPoolSize             = 10
	DefAuthenticationLDAPSASLMaxPoolSize              = 1000
	DefAuthenticationLDAPSimpleAuthMethodName         = "SIMPLE"
	DefAuthenticationLDAPSimpleServerPort             = 389
	DefAuthenticationLDAPSimpleTLS                    = false
	DefAuthenticationLDAPSimpleUserSearchAttr         = "uid"
	DefAuthenticationLDAPSimpleInitPoolSize           = 10
	DefAuthenticationLDAPSimpleMaxPoolSize            = 1000
	DefTiFlashReplicaRead                             = AllReplicaStr
	DefTiDBEnableFastCheckTable                       = true
	DefRuntimeFilterType                              = "IN"
	DefRuntimeFilterMode                              = "OFF"
	DefTiDBLockUnchangedKeys                          = true
	DefTiDBEnableCheckConstraint                      = false
	DefTiDBSkipMissingPartitionStats                  = true
	DefTiDBOptEnableHashJoin                          = true
	DefTiDBHashJoinVersion                            = joinversion.HashJoinVersionOptimized
	DefTiDBOptIndexJoinBuild                          = true
	DefTiDBOptObjective                               = OptObjectiveModerate
	DefTiDBSchemaVersionCacheLimit                    = 16
	DefTiDBIdleTransactionTimeout                     = 0
	DefTiDBTxnEntrySizeLimit                          = 0
	DefTiDBSchemaCacheSize                            = 512 * 1024 * 1024
	DefTiDBLowResolutionTSOUpdateInterval             = 2000
	DefDivPrecisionIncrement                          = 4
	DefTiDBDMLType                                    = "STANDARD"
	DefGroupConcatMaxLen                              = uint64(1024)
	DefDefaultWeekFormat                              = "0"
	DefTiFlashPreAggMode                              = ForcePreAggStr
	DefTiDBEnableLazyCursorFetch                      = false
	DefOptEnableProjectionPushDown                    = true
	DefTiDBEnableSharedLockPromotion                  = false
	DefTiDBTSOClientRPCMode                           = TSOClientRPCModeDefault
	DefTiDBCircuitBreakerPDMetaErrorRateRatio         = 0.0
	DefTiDBAccelerateUserCreationUpdate               = false
	DefTiDBEnableTSValidation                         = true
	DefTiDBLoadBindingTimeout                         = 200
)

// Process global variables.
var (
	ProcessGeneralLog              = atomic.NewBool(false)
	RunAutoAnalyze                 = atomic.NewBool(DefTiDBEnableAutoAnalyze)
	EnableAutoAnalyzePriorityQueue = atomic.NewBool(DefTiDBEnableAutoAnalyzePriorityQueue)
	// AnalyzeColumnOptions is a global variable that indicates the default column choice for ANALYZE.
	// The value of this variable is a string that can be one of the following values:
	// "PREDICATE", "ALL".
	// The behavior of the analyze operation depends on the value of `tidb_persist_analyze_options`:
	// 1. If `tidb_persist_analyze_options` is enabled and the column choice from the analyze options record is set to `default`,
	//    the value of `tidb_analyze_column_options` determines the behavior of the analyze operation.
	// 2. If `tidb_persist_analyze_options` is disabled, `tidb_analyze_column_options` is used directly to decide
	//    whether to analyze all columns or just the predicate columns.
	AnalyzeColumnOptions          = atomic.NewString(DefTiDBAnalyzeColumnOptions)
	GlobalLogMaxDays              = atomic.NewInt32(int32(config.GetGlobalConfig().Log.File.MaxDays))
	QueryLogMaxLen                = atomic.NewInt32(DefTiDBQueryLogMaxLen)
	EnablePProfSQLCPU             = atomic.NewBool(false)
	EnableBatchDML                = atomic.NewBool(false)
	EnableTmpStorageOnOOM         = atomic.NewBool(DefTiDBEnableTmpStorageOnOOM)
	DDLReorgWorkerCounter   int32 = DefTiDBDDLReorgWorkerCount
	DDLReorgBatchSize       int32 = DefTiDBDDLReorgBatchSize
	DDLFlashbackConcurrency int32 = DefTiDBDDLFlashbackConcurrency
	DDLErrorCountLimit      int64 = DefTiDBDDLErrorCountLimit
	DDLReorgRowFormat       int64 = DefTiDBRowFormatV2
	DDLReorgMaxWriteSpeed         = atomic.NewInt64(DefTiDBDDLReorgMaxWriteSpeed)
	MaxDeltaSchemaCount     int64 = DefTiDBMaxDeltaSchemaCount
	// DDLSlowOprThreshold is the threshold for ddl slow operations, uint is millisecond.
	DDLSlowOprThreshold                  = config.GetGlobalConfig().Instance.DDLSlowOprThreshold
	ForcePriority                        = int32(DefTiDBForcePriority)
	MaxOfMaxAllowedPacket         uint64 = 1073741824
	ExpensiveQueryTimeThreshold   uint64 = DefTiDBExpensiveQueryTimeThreshold
	ExpensiveTxnTimeThreshold     uint64 = DefTiDBExpensiveTxnTimeThreshold
	MemoryUsageAlarmRatio                = atomic.NewFloat64(DefMemoryUsageAlarmRatio)
	MemoryUsageAlarmKeepRecordNum        = atomic.NewInt64(DefMemoryUsageAlarmKeepRecordNum)
	EnableLocalTxn                       = atomic.NewBool(DefTiDBEnableLocalTxn)
	MaxTSOBatchWaitInterval              = atomic.NewFloat64(DefTiDBTSOClientBatchMaxWaitTime)
	EnableTSOFollowerProxy               = atomic.NewBool(DefTiDBEnableTSOFollowerProxy)
	EnablePDFollowerHandleRegion         = atomic.NewBool(DefPDEnableFollowerHandleRegion)
	EnableBatchQueryRegion               = atomic.NewBool(DefTiDBEnableBatchQueryRegion)
	RestrictedReadOnly                   = atomic.NewBool(DefTiDBRestrictedReadOnly)
	VarTiDBSuperReadOnly                 = atomic.NewBool(DefTiDBSuperReadOnly)
	PersistAnalyzeOptions                = atomic.NewBool(DefTiDBPersistAnalyzeOptions)
	TableCacheLease                      = atomic.NewInt64(DefTiDBTableCacheLease)
	StatsLoadSyncWait                    = atomic.NewInt64(DefTiDBStatsLoadSyncWait)
	StatsLoadPseudoTimeout               = atomic.NewBool(DefTiDBStatsLoadPseudoTimeout)
	MemQuotaBindingCache                 = atomic.NewInt64(DefTiDBMemQuotaBindingCache)
	GCMaxWaitTime                        = atomic.NewInt64(DefTiDBGCMaxWaitTime)
	StatsCacheMemQuota                   = atomic.NewInt64(DefTiDBStatsCacheMemQuota)
	OOMAction                            = atomic.NewString(DefTiDBMemOOMAction)
	MaxAutoAnalyzeTime                   = atomic.NewInt64(DefTiDBMaxAutoAnalyzeTime)
	// variables for plan cache
	PreparedPlanCacheMemoryGuardRatio   = atomic.NewFloat64(DefTiDBPrepPlanCacheMemoryGuardRatio)
	EnableInstancePlanCache             = atomic.NewBool(false)
	InstancePlanCacheReservedPercentage = atomic.NewFloat64(0.1)
	InstancePlanCacheMaxMemSize         = atomic.NewInt64(int64(DefTiDBInstancePlanCacheMaxMemSize))
	EnableDistTask                      = atomic.NewBool(DefTiDBEnableDistTask)
	EnableFastCreateTable               = atomic.NewBool(DefTiDBEnableFastCreateTable)
	EnableNoopVariables                 = atomic.NewBool(DefTiDBEnableNoopVariables)
	EnableMDL                           = atomic.NewBool(false)
	AutoAnalyzePartitionBatchSize       = atomic.NewInt64(DefTiDBAutoAnalyzePartitionBatchSize)
	AutoAnalyzeConcurrency              = atomic.NewInt32(DefTiDBAutoAnalyzeConcurrency)
	// TODO: set value by session variable
	EnableWorkloadBasedLearning   = atomic.NewBool(DefTiDBEnableWorkloadBasedLearning)
	WorkloadBasedLearningInterval = atomic.NewDuration(DefTiDBWorkloadBasedLearningInterval)
	// EnableFastReorg indicates whether to use lightning to enhance DDL reorg performance.
	EnableFastReorg = atomic.NewBool(DefTiDBEnableFastReorg)
	// DDLDiskQuota is the temporary variable for set disk quota for lightning
	DDLDiskQuota = atomic.NewUint64(DefTiDBDDLDiskQuota)
	// EnableForeignKey indicates whether to enable foreign key feature.
	EnableForeignKey    = atomic.NewBool(true)
	EnableRCReadCheckTS = atomic.NewBool(false)
	// EnableRowLevelChecksum indicates whether to append checksum to row values.
	EnableRowLevelChecksum         = atomic.NewBool(DefTiDBEnableRowLevelChecksum)
	LowResolutionTSOUpdateInterval = atomic.NewUint32(DefTiDBLowResolutionTSOUpdateInterval)

	// DefTiDBServerMemoryLimit indicates the default value of TiDBServerMemoryLimit(TotalMem * 80%).
	// It should be a const and shouldn't be modified after tidb is started.
	DefTiDBServerMemoryLimit           = serverMemoryLimitDefaultValue()
	GOGCTunerThreshold                 = atomic.NewFloat64(DefTiDBGOGCTunerThreshold)
	PasswordValidationLength           = atomic.NewInt32(8)
	PasswordValidationMixedCaseCount   = atomic.NewInt32(1)
	PasswordValidtaionNumberCount      = atomic.NewInt32(1)
	PasswordValidationSpecialCharCount = atomic.NewInt32(1)
	EnableTTLJob                       = atomic.NewBool(DefTiDBTTLJobEnable)
	TTLScanBatchSize                   = atomic.NewInt64(DefTiDBTTLScanBatchSize)
	TTLDeleteBatchSize                 = atomic.NewInt64(DefTiDBTTLDeleteBatchSize)
	TTLDeleteRateLimit                 = atomic.NewInt64(DefTiDBTTLDeleteRateLimit)
	TTLJobScheduleWindowStartTime      = atomic.NewTime(
		mustParseTime(
			FullDayTimeFormat,
			DefTiDBTTLJobScheduleWindowStartTime,
		),
	)
	TTLJobScheduleWindowEndTime = atomic.NewTime(
		mustParseTime(
			FullDayTimeFormat,
			DefTiDBTTLJobScheduleWindowEndTime,
		),
	)
	TTLScanWorkerCount              = atomic.NewInt32(DefTiDBTTLScanWorkerCount)
	TTLDeleteWorkerCount            = atomic.NewInt32(DefTiDBTTLDeleteWorkerCount)
	PasswordHistory                 = atomic.NewInt64(DefPasswordReuseHistory)
	PasswordReuseInterval           = atomic.NewInt64(DefPasswordReuseTime)
	IsSandBoxModeEnabled            = atomic.NewBool(false)
	MaxUserConnectionsValue         = atomic.NewUint32(DefMaxUserConnections)
	MaxPreparedStmtCountValue       = atomic.NewInt64(DefMaxPreparedStmtCount)
	HistoricalStatsDuration         = atomic.NewDuration(DefTiDBHistoricalStatsDuration)
	EnableHistoricalStatsForCapture = atomic.NewBool(DefTiDBEnableHistoricalStatsForCapture)
	TTLRunningTasks                 = atomic.NewInt32(DefTiDBTTLRunningTasks)
	// always set the default value to false because the resource control in kv-client is not inited
	// It will be initialized to the right value after the first call of `rebuildSysVarCache`
	EnableResourceControl           = atomic.NewBool(false)
	EnableResourceControlStrictMode = atomic.NewBool(true)
	EnableCheckConstraint           = atomic.NewBool(DefTiDBEnableCheckConstraint)
	SkipMissingPartitionStats       = atomic.NewBool(DefTiDBSkipMissingPartitionStats)
	TiFlashEnablePipelineMode       = atomic.NewBool(DefTiDBEnableTiFlashPipelineMode)
	ServiceScope                    = atomic.NewString("")
	SchemaVersionCacheLimit         = atomic.NewInt64(DefTiDBSchemaVersionCacheLimit)
	CloudStorageURI                 = atomic.NewString("")
	IgnoreInlistPlanDigest          = atomic.NewBool(DefTiDBIgnoreInlistPlanDigest)
	TxnEntrySizeLimit               = atomic.NewUint64(DefTiDBTxnEntrySizeLimit)

	SchemaCacheSize              = atomic.NewUint64(DefTiDBSchemaCacheSize)
	SchemaCacheSizeOriginText    = atomic.NewString(strconv.Itoa(DefTiDBSchemaCacheSize))
	AccelerateUserCreationUpdate = atomic.NewBool(DefTiDBAccelerateUserCreationUpdate)

	CircuitBreakerPDMetadataErrorRateThresholdRatio = atomic.NewFloat64(0.0)
)

func serverMemoryLimitDefaultValue() string {
	total, err := memory.MemTotal()
	if err == nil && total != 0 {
		return "80%"
	}
	return "0"
}

func mustParseDuration(str string) time.Duration {
	duration, err := time.ParseDuration(str)
	if err != nil {
		panic(fmt.Sprintf("%s is not a duration", str))
	}

	return duration
}

func mustParseTime(layout string, str string) time.Time {
	time, err := time.ParseInLocation(layout, str, time.UTC)
	if err != nil {
		panic(fmt.Sprintf("%s is not in %s duration format", str, layout))
	}

	return time
}

const (
	// OptObjectiveModerate is a possible value and the default value for TiDBOptObjective.
	// Please see comments of SessionVars.OptObjective for details.
	OptObjectiveModerate string = "moderate"
	// OptObjectiveDeterminate is a possible value for TiDBOptObjective.
	OptObjectiveDeterminate = "determinate"
)

// ForcePreAggStr means 1st hashagg will be pre aggregated.
// AutoStr means TiFlash will decide which policy for 1st hashagg.
// ForceStreamingStr means 1st hashagg will for pass through all blocks.
const (
	ForcePreAggStr    = "force_preagg"
	AutoStr           = "auto"
	ForceStreamingStr = "force_streaming"
)

const (
	// AllReplicaStr is the string value of AllReplicas.
	AllReplicaStr = "all_replicas"
	// ClosestAdaptiveStr is the string value of ClosestAdaptive.
	ClosestAdaptiveStr = "closest_adaptive"
	// ClosestReplicasStr is the string value of ClosestReplicas.
	ClosestReplicasStr = "closest_replicas"
)

const (
	// DispatchPolicyRRStr is string value for DispatchPolicyRR.
	DispatchPolicyRRStr = "round_robin"
	// DispatchPolicyConsistentHashStr is string value for DispatchPolicyConsistentHash.
	DispatchPolicyConsistentHashStr = "consistent_hash"
	// DispatchPolicyInvalidStr is string value for DispatchPolicyInvalid.
	DispatchPolicyInvalidStr = "invalid"
)

// ConcurrencyUnset means the value the of the concurrency related variable is unset.
const ConcurrencyUnset = -1

// ExchangeCompressionMode means the compress method used in exchange operator
type ExchangeCompressionMode int

const (
	// ExchangeCompressionModeNONE indicates no compression
	ExchangeCompressionModeNONE ExchangeCompressionMode = iota
	// ExchangeCompressionModeFast indicates fast compression/decompression speed, compression ratio is lower than HC mode
	ExchangeCompressionModeFast
	// ExchangeCompressionModeHC indicates high compression (HC) ratio mode
	ExchangeCompressionModeHC
	// ExchangeCompressionModeUnspecified indicates unspecified compress method, let TiDB choose one
	ExchangeCompressionModeUnspecified

	// RecommendedExchangeCompressionMode indicates recommended compression mode
	RecommendedExchangeCompressionMode ExchangeCompressionMode = ExchangeCompressionModeFast

	exchangeCompressionModeUnspecifiedName string = "UNSPECIFIED"
)

// Name returns the name of ExchangeCompressionMode
func (t ExchangeCompressionMode) Name() string {
	if t == ExchangeCompressionModeUnspecified {
		return exchangeCompressionModeUnspecifiedName
	}
	return t.ToTipbCompressionMode().String()
}

// ToExchangeCompressionMode returns the ExchangeCompressionMode from name
func ToExchangeCompressionMode(name string) (ExchangeCompressionMode, bool) {
	name = strings.ToUpper(name)
	if name == exchangeCompressionModeUnspecifiedName {
		return ExchangeCompressionModeUnspecified, true
	}
	value, ok := tipb.CompressionMode_value[name]
	if ok {
		return ExchangeCompressionMode(value), true
	}
	return ExchangeCompressionModeNONE, false
}

// ToTipbCompressionMode returns tipb.CompressionMode from kv.ExchangeCompressionMode
func (t ExchangeCompressionMode) ToTipbCompressionMode() tipb.CompressionMode {
	switch t {
	case ExchangeCompressionModeNONE:
		return tipb.CompressionMode_NONE
	case ExchangeCompressionModeFast:
		return tipb.CompressionMode_FAST
	case ExchangeCompressionModeHC:
		return tipb.CompressionMode_HIGH_COMPRESSION
	}
	return tipb.CompressionMode_NONE
}

// ScopeFlag is for system variable whether can be changed in global/session dynamically or not.
type ScopeFlag uint8

// TypeFlag is the SysVar type, which doesn't exactly match MySQL types.
type TypeFlag byte

const (
	// ScopeNone means the system variable can not be changed dynamically.
	ScopeNone ScopeFlag = 0
	// ScopeGlobal means the system variable can be changed globally.
	ScopeGlobal ScopeFlag = 1 << 0
	// ScopeSession means the system variable can only be changed in current session.
	ScopeSession ScopeFlag = 1 << 1
	// ScopeInstance means it is similar to global but doesn't propagate to other TiDB servers.
	ScopeInstance ScopeFlag = 1 << 2

	// TypeStr is the default
	TypeStr TypeFlag = iota
	// TypeBool for boolean
	TypeBool
	// TypeInt for integer
	TypeInt
	// TypeEnum for Enum
	TypeEnum
	// TypeFloat for Double
	TypeFloat
	// TypeUnsigned for Unsigned integer
	TypeUnsigned
	// TypeTime for time of day (a TiDB extension)
	TypeTime
	// TypeDuration for a golang duration (a TiDB extension)
	TypeDuration

	// On is the canonical string for ON
	On = "ON"
	// Off is the canonical string for OFF
	Off = "OFF"
	// Warn means return warnings
	Warn = "WARN"
	// IntOnly means enable for int type
	IntOnly = "INT_ONLY"
	// Marker is a special log redact behavior
	Marker = "MARKER"

	// AssertionStrictStr is a choice of variable TiDBTxnAssertionLevel that means full assertions should be performed,
	// even if the performance might be slowed down.
	AssertionStrictStr = "STRICT"
	// AssertionFastStr is a choice of variable TiDBTxnAssertionLevel that means assertions that doesn't affect
	// performance should be performed.
	AssertionFastStr = "FAST"
	// AssertionOffStr is a choice of variable TiDBTxnAssertionLevel that means no assertion should be performed.
	AssertionOffStr = "OFF"
	// OOMActionCancel constants represents the valid action configurations for OOMAction "CANCEL".
	OOMActionCancel = "CANCEL"
	// OOMActionLog constants represents the valid action configurations for OOMAction "LOG".
	OOMActionLog = "LOG"

	// TSOClientRPCModeDefault is a choice of variable TiDBTSOClientRPCMode. In this mode, the TSO client sends batched
	// TSO requests serially.
	TSOClientRPCModeDefault = "DEFAULT"
	// TSOClientRPCModeParallel is a choice of variable TiDBTSOClientRPCMode. In this mode, the TSO client tries to
	// keep approximately 2 batched TSO requests running in parallel. This option tries to reduce the batch-waiting time
	// by half, at the expense of about twice the amount of TSO RPC calls.
	TSOClientRPCModeParallel = "PARALLEL"
	// TSOClientRPCModeParallelFast is a choice of variable TiDBTSOClientRPCMode. In this mode, the TSO client tries to
	// keep approximately 4 batched TSO requests running in parallel. This option tries to reduce the batch-waiting time
	// by 3/4, at the expense of about 4 times the amount of TSO RPC calls.
	TSOClientRPCModeParallelFast = "PARALLEL-FAST"

	// StrategyStandard is a choice of variable TiDBPipelinedDmlResourcePolicy,
	// the best performance policy
	StrategyStandard = "standard"
	// StrategyConservative is a choice of variable TiDBPipelinedDmlResourcePolicy,
	// a rather conservative policy
	StrategyConservative = "conservative"
	// StrategyCustom is a choice of variable TiDBPipelinedDmlResourcePolicy,
	StrategyCustom = "custom"
)

// Global config name list.
const (
	GlobalConfigEnableTopSQL = "enable_resource_metering"
	GlobalConfigSourceID     = "source_id"
)

func (s ScopeFlag) String() string {
	var scopes []string
	if s == ScopeNone {
		return "NONE"
	}
	if s&ScopeSession != 0 {
		scopes = append(scopes, "SESSION")
	}
	if s&ScopeGlobal != 0 {
		scopes = append(scopes, "GLOBAL")
	}
	if s&ScopeInstance != 0 {
		scopes = append(scopes, "INSTANCE")
	}
	return strings.Join(scopes, ",")
}

// ClusteredIndexDefMode controls the default clustered property for primary key.
type ClusteredIndexDefMode int

const (
	// ClusteredIndexDefModeIntOnly indicates only single int primary key will default be clustered.
	ClusteredIndexDefModeIntOnly ClusteredIndexDefMode = 0
	// ClusteredIndexDefModeOn indicates primary key will default be clustered.
	ClusteredIndexDefModeOn ClusteredIndexDefMode = 1
	// ClusteredIndexDefModeOff indicates primary key will default be non-clustered.
	ClusteredIndexDefModeOff ClusteredIndexDefMode = 2
)

// TiDBOptEnableClustered converts enable clustered options to ClusteredIndexDefMode.
func TiDBOptEnableClustered(opt string) ClusteredIndexDefMode {
	switch opt {
	case On:
		return ClusteredIndexDefModeOn
	case Off:
		return ClusteredIndexDefModeOff
	default:
		return ClusteredIndexDefModeIntOnly
	}
}

const (
	// ScatterOff means default, will not scatter region
	ScatterOff string = ""
	// ScatterTable means scatter region at table level
	ScatterTable string = "table"
	// ScatterGlobal means scatter region at global level
	ScatterGlobal string = "global"
)

const (
	// PlacementModeStrict indicates all placement operations should be checked strictly in ddl
	PlacementModeStrict string = "STRICT"
	// PlacementModeIgnore indicates ignore all placement operations in ddl
	PlacementModeIgnore string = "IGNORE"
)

const (
	// LocalDayTimeFormat is the local format of analyze start time and end time.
	LocalDayTimeFormat = "15:04"
	// FullDayTimeFormat is the full format of analyze start time and end time.
	FullDayTimeFormat = "15:04 -0700"
)

// SetDDLReorgWorkerCounter sets DDLReorgWorkerCounter count.
// Sysvar validation enforces the range to already be correct.
func SetDDLReorgWorkerCounter(cnt int32) {
	goatomic.StoreInt32(&DDLReorgWorkerCounter, cnt)
}

// GetDDLReorgWorkerCounter gets DDLReorgWorkerCounter.
func GetDDLReorgWorkerCounter() int32 {
	return goatomic.LoadInt32(&DDLReorgWorkerCounter)
}

// SetDDLFlashbackConcurrency sets DDLFlashbackConcurrency count.
// Sysvar validation enforces the range to already be correct.
func SetDDLFlashbackConcurrency(cnt int32) {
	goatomic.StoreInt32(&DDLFlashbackConcurrency, cnt)
}

// GetDDLFlashbackConcurrency gets DDLFlashbackConcurrency count.
func GetDDLFlashbackConcurrency() int32 {
	return goatomic.LoadInt32(&DDLFlashbackConcurrency)
}

// SetDDLReorgBatchSize sets DDLReorgBatchSize size.
// Sysvar validation enforces the range to already be correct.
func SetDDLReorgBatchSize(cnt int32) {
	goatomic.StoreInt32(&DDLReorgBatchSize, cnt)
}

// GetDDLReorgBatchSize gets DDLReorgBatchSize.
func GetDDLReorgBatchSize() int32 {
	return goatomic.LoadInt32(&DDLReorgBatchSize)
}

// SetDDLErrorCountLimit sets ddlErrorCountlimit size.
func SetDDLErrorCountLimit(cnt int64) {
	goatomic.StoreInt64(&DDLErrorCountLimit, cnt)
}

// GetDDLErrorCountLimit gets ddlErrorCountlimit size.
func GetDDLErrorCountLimit() int64 {
	return goatomic.LoadInt64(&DDLErrorCountLimit)
}

// SetDDLReorgRowFormat sets DDLReorgRowFormat version.
func SetDDLReorgRowFormat(format int64) {
	goatomic.StoreInt64(&DDLReorgRowFormat, format)
}

// GetDDLReorgRowFormat gets DDLReorgRowFormat version.
func GetDDLReorgRowFormat() int64 {
	return goatomic.LoadInt64(&DDLReorgRowFormat)
}

// SetMaxDeltaSchemaCount sets MaxDeltaSchemaCount size.
func SetMaxDeltaSchemaCount(cnt int64) {
	goatomic.StoreInt64(&MaxDeltaSchemaCount, cnt)
}

// GetMaxDeltaSchemaCount gets MaxDeltaSchemaCount size.
func GetMaxDeltaSchemaCount() int64 {
	return goatomic.LoadInt64(&MaxDeltaSchemaCount)
}
