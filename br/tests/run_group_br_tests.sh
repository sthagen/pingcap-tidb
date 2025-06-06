#!/usr/bin/env bash

# This script split the integration tests into 9 groups to support parallel group tests execution.
# all the integration tests are located in br/tests directory. only the directories
# containing run.sh will be considered as valid br integration tests. the script will print the total case number

set -eo pipefail

# Step 1
CUR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
group=$1
export COV_DIR="/tmp/group_cover"
rm -rf $COV_DIR
mkdir -p $COV_DIR

# Define groups
# Note: If new group is added, the group name must also be added to CI
# * https://github.com/PingCAP-QE/ci/blob/main/pipelines/pingcap/tidb/latest/pull_br_integration_test.groovy
# Each group of tests consumes as much time as possible, thus reducing CI waiting time.
# Putting multiple light tests together and heavy tests in a separate group.
declare -A groups
groups=(
	["G00"]="br_300_small_tables br_backup_empty br_backup_version br_cache_table br_case_sensitive br_charset_gbk br_check_new_collocation_enable br_history br_gcs br_rawkv br_tidb_placement_policy"
	["G01"]="br_autoid br_crypter2 br_db br_check_dup_table br_db_online br_db_online_newkv br_db_skip br_debug_meta br_ebs br_foreign_key br_full br_table_partition br_full_ddl br_tiflash"
	["G02"]="br_full_cluster_restore br_full_index br_incremental_ddl br_pitr_failpoint br_pitr_gc_safepoint br_other br_pitr_long_running_schema_loading br_parallel_restore"
	["G03"]='br_incompatible_tidb_config br_incremental br_incremental_index br_incremental_only_ddl br_incremental_same_table br_insert_after_restore br_key_locked br_log_test br_move_backup br_mv_index'
	["G04"]='br_range br_replica_read br_restore_TDE_enable br_restore_log_task_enable br_s3 br_shuffle_leader br_shuffle_region br_single_table br_region_rule'
	["G05"]='br_skip_checksum br_split_region_fail br_systables br_table_filter br_txn br_stats br_clustered_index br_crypter br_partition_add_index br_pitr_log_restore_backup_compatibility'
	["G06"]='br_tikv_outage br_tikv_outage3 br_restore_checkpoint br_encryption br_pitr_online_table_filter'
	["G07"]='br_pitr br_restore_physical'
	["G08"]='br_tikv_outage2 br_ttl br_views_and_sequences br_z_gc_safepoint br_autorandom br_file_corruption br_tiflash_conflict br_pitr_table_filter'
)

# Get other cases not in groups, to avoid missing any case
others=()
for script in "$CUR"/*/run.sh; do
	test_name="$(basename "$(dirname "$script")")"
	if [[ $test_name != br* ]]; then
		continue
	fi
	# shellcheck disable=SC2076
	if [[ ! " ${groups[*]} " =~ " ${test_name} " ]]; then
		others=("${others[@]} ${test_name}")
	fi
done

# enable local encryption for all tests
ENABLE_ENCRYPTION=true
export ENABLE_ENCRYPTION

if [[ "$group" == "others" ]]; then
	if [[ -z $others ]]; then
		echo "All br integration test cases have been added to groups"
		exit 0
	fi
	echo "Error: "$others" is not added to any group in br/tests/run_group_br_tests.sh"
	exit 1
elif [[ " ${!groups[*]} " =~ " ${group} " ]]; then
	test_names="${groups[${group}]}"
	# Run test cases
	if [[ -n $test_names ]]; then
		echo ""
		echo "Run cases: ${test_names}"
        for case_name in $test_names; do
            echo "Run cases: ${case_name}"
            rm -rf /tmp/backup_restore_test
            mkdir -p /tmp/backup_restore_test
            TEST_NAME=${case_name} ${CUR}/run.sh
        done
	fi
else
	echo "Error: invalid group name: ${group}"
	exit 1
fi
