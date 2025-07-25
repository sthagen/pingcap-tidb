// Copyright 2021 PingCAP, Inc.
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

package session

import (
	"cmp"
	"context"
	"crypto/tls"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/kvproto/pkg/keyspacepb"
	"github.com/pingcap/log"
	"github.com/pingcap/tidb/pkg/ddl"
	"github.com/pingcap/tidb/pkg/domain"
	"github.com/pingcap/tidb/pkg/expression/sessionexpr"
	"github.com/pingcap/tidb/pkg/keyspace"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/meta"
	"github.com/pingcap/tidb/pkg/meta/metadef"
	"github.com/pingcap/tidb/pkg/parser/auth"
	"github.com/pingcap/tidb/pkg/session/sessionapi"
	"github.com/pingcap/tidb/pkg/sessionctx"
	"github.com/pingcap/tidb/pkg/sessionctx/vardef"
	"github.com/pingcap/tidb/pkg/sessionctx/variable"
	"github.com/pingcap/tidb/pkg/statistics"
	"github.com/pingcap/tidb/pkg/store/mockstore"
	"github.com/pingcap/tidb/pkg/table/tblsession"
	"github.com/pingcap/tidb/pkg/telemetry"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/tests/v3/integration"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestMySQLDBTables(t *testing.T) {
	require.Len(t, tablesInSystemDatabase, 52,
		"remember to add the new tables to versionedBootstrapSchemas too")
	testTableBasicInfoSlice(t, tablesInSystemDatabase)
	reservedIDs := make([]int64, 0, len(ddlTableVersionTables)*2)
	for _, v := range ddlTableVersionTables {
		for _, tbl := range v.tables {
			reservedIDs = append(reservedIDs, tbl.ID)
		}
	}
	for _, tbl := range tablesInSystemDatabase {
		reservedIDs = append(reservedIDs, tbl.ID)
	}
	for _, db := range systemDatabases {
		reservedIDs = append(reservedIDs, db.ID)
	}
	slices.Sort(reservedIDs)
	require.IsIncreasing(t, reservedIDs, "used IDs should be in increasing order")
	require.Greater(t, reservedIDs[0], metadef.ReservedGlobalIDLowerBound, "reserved ID should be greater than ReservedGlobalIDLowerBound")
	require.LessOrEqual(t, reservedIDs[len(reservedIDs)-1], metadef.ReservedGlobalIDUpperBound, "reserved ID should be less than or equal to ReservedGlobalIDUpperBound")
}

// This test file have many problem.
// 1. Please use testkit to create dom, session and store.
// 2. Don't use CreateStoreAndBootstrap and BootstrapSession together. It will cause data race.
// Please do not add any test here. You can add test case at the bootstrap_update_test.go. After All problem fixed,
// We will overwrite this file by update_test.go.
func TestBootstrap(t *testing.T) {
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()
	defer dom.Close()
	se := CreateSessionAndSetID(t, store)
	MustExec(t, se, "set global tidb_txn_mode=''")
	MustExec(t, se, "use mysql")
	r := MustExecToRecodeSet(t, se, "select * from user")
	require.NotNil(t, r)

	ctx := context.Background()
	req := r.NewChunk(nil)
	err := r.Next(ctx, req)
	require.NoError(t, err)
	require.NotEqual(t, 0, req.NumRows())

	rows := statistics.RowToDatums(req.GetRow(0), r.Fields())
	match(t, rows, `%`, "root", "", "mysql_native_password", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "N", "Y", "Y", "Y", "Y", "Y", nil, nil, nil, "", "N", time.Now(), nil, 0)
	r.Close()

	require.NoError(t, se.Auth(&auth.UserIdentity{Username: "root", Hostname: "anyhost"}, []byte(""), []byte(""), nil))

	MustExec(t, se, "use test")

	// Check privilege tables.
	MustExec(t, se, "SELECT * from mysql.global_priv")
	MustExec(t, se, "SELECT * from mysql.db")
	MustExec(t, se, "SELECT * from mysql.tables_priv")
	MustExec(t, se, "SELECT * from mysql.columns_priv")
	MustExec(t, se, "SELECT * from mysql.global_grants")

	// Check privilege tables.
	r = MustExecToRecodeSet(t, se, "SELECT COUNT(*) from mysql.global_variables")
	require.NotNil(t, r)

	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, globalVarsCount(), req.GetRow(0).GetInt64(0))
	require.NoError(t, r.Close())

	// Check a storage operations are default autocommit after the second start.
	MustExec(t, se, "USE test")
	MustExec(t, se, "drop table if exists t")
	MustExec(t, se, "create table t (id int)")
	store.SetOption(StoreBootstrappedKey, nil)
	se.Close()

	se, err = CreateSession4Test(store)
	require.NoError(t, err)
	MustExec(t, se, "USE test")
	MustExec(t, se, "insert t values (?)", 3)

	se, err = CreateSession4Test(store)
	require.NoError(t, err)
	MustExec(t, se, "USE test")
	r = MustExecToRecodeSet(t, se, "select * from t")
	require.NotNil(t, r)

	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	rows = statistics.RowToDatums(req.GetRow(0), r.Fields())
	match(t, rows, 3)
	MustExec(t, se, "drop table if exists t")
	se.Close()

	// Try to do bootstrap dml jobs on an already bootstrapped TiDB system will not cause fatal.
	// For https://github.com/pingcap/tidb/issues/1096
	se, err = CreateSession4Test(store)
	require.NoError(t, err)
	doDMLWorks(se)
	r = MustExecToRecodeSet(t, se, "select * from mysql.expr_pushdown_blacklist where name = 'date_add'")
	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 0, req.NumRows())
	se.Close()
}

func globalVarsCount() int64 {
	var count int64
	for _, v := range variable.GetSysVars() {
		if v.HasGlobalScope() {
			count++
		}
	}
	return count
}

// testBootstrapWithError :
// When a session failed in bootstrap process (for example, the session is killed after doDDLWorks()).
// We should make sure that the following session could finish the bootstrap process.
func TestBootstrapWithError(t *testing.T) {
	ctx := context.Background()
	store, err := mockstore.NewMockStore(mockstore.WithStoreType(mockstore.EmbedUnistore))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	// bootstrap
	{
		se := &session{
			store:       store,
			sessionVars: variable.NewSessionVars(nil),
		}
		se.exprctx = sessionexpr.NewExprContext(se)
		se.pctx = newPlanContextImpl(se)
		se.tblctx = tblsession.NewMutateContext(se)
		globalVarsAccessor := variable.NewMockGlobalAccessor4Tests()
		se.GetSessionVars().GlobalVarsAccessor = globalVarsAccessor
		se.functionUsageMu.builtinFunctionUsage = make(telemetry.BuiltinFunctionsUsage)
		se.txn.init()
		se.mu.values = make(map[fmt.Stringer]any)
		se.SetValue(sessionctx.Initing, true)
		err := InitDDLTables(store)
		require.NoError(t, err)
		dom, err := domap.Get(store)
		require.NoError(t, err)
		require.NoError(t, dom.Start(ddl.Bootstrap))
		se.dom = dom
		se.infoCache = dom.InfoCache()
		se.schemaValidator = dom.GetSchemaValidator()
		b, err := checkBootstrapped(se)
		require.False(t, b)
		require.NoError(t, err)
		doDDLWorks(se)
	}

	dom, err := domap.Get(store)
	require.NoError(t, err)
	dom.Close()

	dom1, err := BootstrapSession(store)
	require.NoError(t, err)
	defer dom1.Close()

	se := CreateSessionAndSetID(t, store)
	MustExec(t, se, "USE mysql")
	r := MustExecToRecodeSet(t, se, `select * from user`)
	req := r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.NotEqual(t, 0, req.NumRows())

	row := req.GetRow(0)
	rows := statistics.RowToDatums(row, r.Fields())
	match(t, rows, `%`, "root", "", "mysql_native_password", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "Y", "N", "Y", "Y", "Y", "Y", "Y", nil, nil, nil, "", "N", time.Now(), nil, 0)
	require.NoError(t, r.Close())

	MustExec(t, se, "USE test")
	// Check privilege tables.
	MustExec(t, se, "SELECT * from mysql.global_priv")
	MustExec(t, se, "SELECT * from mysql.db")
	MustExec(t, se, "SELECT * from mysql.tables_priv")
	MustExec(t, se, "SELECT * from mysql.columns_priv")
	// Check role tables.
	MustExec(t, se, "SELECT * from mysql.role_edges")
	MustExec(t, se, "SELECT * from mysql.default_roles")
	// Check global variables.
	r = MustExecToRecodeSet(t, se, "SELECT COUNT(*) from mysql.global_variables")
	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	v := req.GetRow(0)
	require.Equal(t, globalVarsCount(), v.GetInt64(0))
	require.NoError(t, r.Close())

	r = MustExecToRecodeSet(t, se, `SELECT VARIABLE_VALUE from mysql.TiDB where VARIABLE_NAME="bootstrapped"`)
	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.NotEqual(t, 0, req.NumRows())
	row = req.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, []byte("True"), row.GetBytes(0))
	require.NoError(t, r.Close())

	MustExec(t, se, "SELECT * from mysql.tidb_background_subtask")
	MustExec(t, se, "SELECT * from mysql.tidb_background_subtask_history")

	// Check tidb_ttl_table_status table
	MustExec(t, se, "SELECT * from mysql.tidb_ttl_table_status")
	// Check mysql.tidb_workload_values table
	MustExec(t, se, "SELECT * from mysql.tidb_workload_values")
}

func TestDDLTableCreateBackfillTable(t *testing.T) {
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()
	se := CreateSessionAndSetID(t, store)

	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	ver, err := m.GetDDLTableVersion()
	require.NoError(t, err)
	require.GreaterOrEqual(t, ver, meta.BackfillTableVersion)

	// downgrade `mDDLTableVersion`
	m.SetDDLTableVersion(meta.MDLTableVersion)
	MustExec(t, se, "drop table mysql.tidb_background_subtask")
	MustExec(t, se, "drop table mysql.tidb_background_subtask_history")
	// TODO(lance6716): remove it after tidb_ddl_notifier GA
	MustExec(t, se, "drop table mysql.tidb_ddl_notifier")
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	// to upgrade session for create ddl related tables
	dom.Close()
	dom, err = BootstrapSession(store)
	require.NoError(t, err)

	se = CreateSessionAndSetID(t, store)
	MustExec(t, se, "select * from mysql.tidb_background_subtask")
	MustExec(t, se, "select * from mysql.tidb_background_subtask_history")
	dom.Close()
}

func TestDDLTableCreateDDLNotifierTable(t *testing.T) {
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()
	se := CreateSessionAndSetID(t, store)

	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	ver, err := m.GetDDLTableVersion()
	require.NoError(t, err)
	require.GreaterOrEqual(t, ver, meta.DDLNotifierTableVersion)

	// downgrade DDL table version
	m.SetDDLTableVersion(meta.BackfillTableVersion)
	MustExec(t, se, "drop table mysql.tidb_ddl_notifier")
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	// to upgrade session for create ddl notifier table
	dom.Close()
	dom, err = BootstrapSession(store)
	require.NoError(t, err)

	se = CreateSessionAndSetID(t, store)
	MustExec(t, se, "select * from mysql.tidb_ddl_notifier")
	dom.Close()
}

func revertVersionAndVariables(t *testing.T, se sessionapi.Session, ver int) {
	MustExec(t, se, fmt.Sprintf("update mysql.tidb set variable_value='%d' where variable_name='tidb_server_version'", ver))
	if ver <= version195 {
		// for version <= version195, tidb_enable_dist_task should be disabled before upgrade
		MustExec(t, se, "update mysql.global_variables set variable_value='off' where variable_name='tidb_enable_dist_task'")
	}
}

// TestUpgrade tests upgrading
func TestUpgrade(t *testing.T) {
	ctx := context.Background()

	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()
	se := CreateSessionAndSetID(t, store)

	MustExec(t, se, "USE mysql")

	// bootstrap with currentBootstrapVersion
	r := MustExecToRecodeSet(t, se, `SELECT VARIABLE_VALUE from mysql.TiDB where VARIABLE_NAME="tidb_server_version"`)
	req := r.NewChunk(nil)
	err := r.Next(ctx, req)
	row := req.GetRow(0)
	require.NoError(t, err)
	require.NotEqual(t, 0, req.NumRows())
	require.Equal(t, 1, row.Len())
	require.Equal(t, fmt.Appendf(nil, "%d", currentBootstrapVersion), row.GetBytes(0))
	require.NoError(t, r.Close())

	se1 := CreateSessionAndSetID(t, store)
	ver, err := getBootstrapVersion(se1)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// Do something to downgrade the store.
	// downgrade meta bootstrap version
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(1))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	MustExec(t, se1, `delete from mysql.TiDB where VARIABLE_NAME="tidb_server_version"`)
	MustExec(t, se1, "update mysql.global_variables set variable_value='off' where variable_name='tidb_enable_dist_task'")
	MustExec(t, se1, fmt.Sprintf(`delete from mysql.global_variables where VARIABLE_NAME="%s"`, vardef.TiDBDistSQLScanConcurrency))
	MustExec(t, se1, `commit`)
	store.SetOption(StoreBootstrappedKey, nil)
	revertVersionAndVariables(t, se1, 0)
	// Make sure the version is downgraded.
	r = MustExecToRecodeSet(t, se1, `SELECT VARIABLE_VALUE from mysql.TiDB where VARIABLE_NAME="tidb_server_version"`)
	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 0, req.NumRows())
	require.NoError(t, r.Close())

	ver, err = getBootstrapVersion(se1)
	require.NoError(t, err)
	require.Equal(t, int64(0), ver)
	dom.Close()
	// Create a new session then upgrade() will run automatically.
	dom, err = BootstrapSession(store)
	require.NoError(t, err)

	se2 := CreateSessionAndSetID(t, store)
	r = MustExecToRecodeSet(t, se2, `SELECT VARIABLE_VALUE from mysql.TiDB where VARIABLE_NAME="tidb_server_version"`)
	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.NotEqual(t, 0, req.NumRows())
	row = req.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, fmt.Appendf(nil, "%d", currentBootstrapVersion), row.GetBytes(0))
	require.NoError(t, r.Close())

	ver, err = getBootstrapVersion(se2)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// Verify that 'new_collation_enabled' is false.
	r = MustExecToRecodeSet(t, se2, fmt.Sprintf(`SELECT VARIABLE_VALUE from mysql.TiDB where VARIABLE_NAME='%s'`, TidbNewCollationEnabled))
	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, req.NumRows())
	require.Equal(t, "False", req.GetRow(0).GetString(0))
	require.NoError(t, r.Close())

	r = MustExecToRecodeSet(t, se2, "admin show ddl jobs 1000;")
	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	rowCnt := req.NumRows()
	for i := range rowCnt {
		jobType := req.GetRow(i).GetString(3) // get job type.
		// Should not use multi-schema change in bootstrap DDL because the job arguments may be changed.
		require.False(t, strings.Contains(jobType, "multi-schema"))
	}
	require.NoError(t, r.Close())

	dom.Close()
}

func TestIssue17979_1(t *testing.T) {
	ctx := context.Background()

	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()
	// test issue 20900, upgrade from v3.0 to v4.0.11+
	seV3 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(58))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV3, 58)
	MustExec(t, seV3, "delete from mysql.tidb where variable_name='default_oom_action'")
	MustExec(t, seV3, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV3)
	require.NoError(t, err)
	require.Equal(t, int64(58), ver)
	dom.Close()
	domV4, err := BootstrapSession(store)
	require.NoError(t, err)
	seV4 := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seV4)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)
	r := MustExecToRecodeSet(t, seV4, "select variable_value from mysql.tidb where variable_name='default_oom_action'")
	req := r.NewChunk(nil)
	require.NoError(t, r.Next(ctx, req))
	require.Equal(t, vardef.OOMActionLog, req.GetRow(0).GetString(0))
	domV4.Close()
}

func TestIssue17979_2(t *testing.T) {
	ctx := context.Background()

	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// test issue 20900, upgrade from v4.0.11 to v4.0.11
	seV3 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(59))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV3, 59)
	MustExec(t, seV3, "delete from mysql.tidb where variable_name='default_iim_action'")
	MustExec(t, seV3, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV3)
	require.NoError(t, err)
	require.Equal(t, int64(59), ver)
	dom.Close()
	domV4, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domV4.Close()
	seV4 := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seV4)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)
	r := MustExecToRecodeSet(t, seV4, "select variable_value from mysql.tidb where variable_name='default_oom_action'")
	req := r.NewChunk(nil)
	require.NoError(t, r.Next(ctx, req))
	require.Equal(t, 0, req.NumRows())
}

// TestIssue20900_2 tests that a user can upgrade from TiDB 2.1 to latest,
// and their configuration remains similar. This helps protect against the
// case that a user had a 32G query memory limit in 2.1, but it is now a 1G limit
// in TiDB 4.0+. I tested this process, and it does correctly upgrade from 2.1 -> 4.0,
// but from 4.0 -> 5.0, the new default is picked up.

func TestIssue20900_2(t *testing.T) {
	ctx := context.Background()

	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// test issue 20900, upgrade from v4.0.8 to v4.0.9+
	seV3 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(52))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV3, 52)
	MustExec(t, seV3, "delete from mysql.tidb where variable_name='default_memory_quota_query'")
	MustExec(t, seV3, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV3)
	require.NoError(t, err)
	require.Equal(t, int64(52), ver)
	dom.Close()
	domV4, err := BootstrapSession(store)
	require.NoError(t, err)
	seV4 := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seV4)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)
	r := MustExecToRecodeSet(t, seV4, "select @@tidb_mem_quota_query")
	req := r.NewChunk(nil)
	require.NoError(t, r.Next(ctx, req))
	require.Equal(t, "1073741824", req.GetRow(0).GetString(0))
	require.Equal(t, int64(1073741824), seV4.GetSessionVars().MemQuotaQuery)
	r = MustExecToRecodeSet(t, seV4, "select variable_value from mysql.tidb where variable_name='default_memory_quota_query'")
	req = r.NewChunk(nil)
	require.NoError(t, r.Next(ctx, req))
	require.Equal(t, 0, req.NumRows())
	domV4.Close()
}

func TestANSISQLMode(t *testing.T) {
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()
	se := CreateSessionAndSetID(t, store)

	MustExec(t, se, "USE mysql")
	MustExec(t, se, `set @@global.sql_mode="NO_AUTO_CREATE_USER,NO_ENGINE_SUBSTITUTION,ANSI"`)
	MustExec(t, se, `delete from mysql.TiDB where VARIABLE_NAME="tidb_server_version"`)
	store.SetOption(StoreBootstrappedKey, nil)
	se.Close()

	// Do some clean up, BootstrapSession will not create a new domain otherwise.
	dom.Close()

	// Set ANSI sql_mode and bootstrap again, to cover a bugfix.
	// Once we have a SQL like that:
	// select variable_value from mysql.tidb where variable_name = "system_tz"
	// it fails to execute in the ANSI sql_mode, and makes TiDB cluster fail to bootstrap.
	dom1, err := BootstrapSession(store)
	require.NoError(t, err)
	defer dom1.Close()
	se = CreateSessionAndSetID(t, store)
	MustExec(t, se, "select @@global.sql_mode")
	se.Close()
}

func TestOldPasswordUpgrade(t *testing.T) {
	pwd := "abc"
	oldpwd := fmt.Sprintf("%X", auth.Sha1Hash([]byte(pwd)))
	newpwd, err := oldPasswordUpgrade(oldpwd)
	require.NoError(t, err)
	require.Equal(t, "*0D3CED9BEC10A777AEC23CCC353A8C08A633045E", newpwd)
}

func TestBootstrapInitExpensiveQueryHandle(t *testing.T) {
	store, _ := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()
	se, err := createSession(store)
	require.NoError(t, err)
	dom := domain.GetDomain(se)
	require.NotNil(t, dom)
	defer dom.Close()
	require.NotNil(t, dom.ExpensiveQueryHandle())
}

func TestStmtSummary(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()
	defer dom.Close()
	se := CreateSessionAndSetID(t, store)

	r := MustExecToRecodeSet(t, se, "select variable_value from mysql.global_variables where variable_name='tidb_enable_stmt_summary'")
	req := r.NewChunk(nil)
	require.NoError(t, r.Next(ctx, req))
	row := req.GetRow(0)
	require.Equal(t, []byte("ON"), row.GetBytes(0))
	require.NoError(t, r.Close())
}

func TestUpgradeClusteredIndexDefaultValue(t *testing.T) {
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	seV67 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(67))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV67, 67)
	MustExec(t, seV67, "UPDATE mysql.global_variables SET VARIABLE_VALUE = 'OFF' where VARIABLE_NAME = 'tidb_enable_clustered_index'")
	require.Equal(t, uint64(1), seV67.GetSessionVars().StmtCtx.AffectedRows())
	MustExec(t, seV67, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV67)
	require.NoError(t, err)
	require.Equal(t, int64(67), ver)
	dom.Close()

	domV68, err := BootstrapSession(store)
	require.NoError(t, err)
	seV68 := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seV68)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	r := MustExecToRecodeSet(t, seV68, `select @@global.tidb_enable_clustered_index, @@session.tidb_enable_clustered_index`)
	req := r.NewChunk(nil)
	require.NoError(t, r.Next(context.Background(), req))
	require.Equal(t, 1, req.NumRows())
	row := req.GetRow(0)
	require.Equal(t, "ON", row.GetString(0))
	require.Equal(t, "ON", row.GetString(1))
	domV68.Close()
}

func TestForIssue23387(t *testing.T) {
	// For issue https://github.com/pingcap/tidb/issues/23387
	saveCurrentBootstrapVersion := currentBootstrapVersion
	currentBootstrapVersion = version57

	// Bootstrap to an old version, create a user.
	store, err := mockstore.NewMockStore()
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	dom, err := BootstrapSession(store)
	require.NoError(t, err)

	se := CreateSessionAndSetID(t, store)
	se.Auth(&auth.UserIdentity{Username: "root", Hostname: `%`}, nil, []byte("012345678901234567890"), nil)
	MustExec(t, se, "create user quatest")
	dom.Close()
	// Upgrade to a newer version, check the user's privilege.
	currentBootstrapVersion = saveCurrentBootstrapVersion
	dom, err = BootstrapSession(store)
	require.NoError(t, err)
	defer dom.Close()

	se = CreateSessionAndSetID(t, store)
	se.Auth(&auth.UserIdentity{Username: "root", Hostname: `%`}, nil, []byte("012345678901234567890"), nil)
	rs, err := exec(se, "show grants for quatest")
	require.NoError(t, err)
	rows, err := ResultSetToStringSlice(context.Background(), se, rs)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "GRANT USAGE ON *.* TO 'quatest'@'%'", rows[0][0])
}

func TestReferencesPrivilegeOnColumn(t *testing.T) {
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()
	defer dom.Close()
	se := CreateSessionAndSetID(t, store)

	defer func() {
		MustExec(t, se, "drop user if exists issue28531")
		MustExec(t, se, "drop table if exists t1")
	}()

	MustExec(t, se, "create user if not exists issue28531")
	MustExec(t, se, "use test")
	MustExec(t, se, "drop table if exists t1")
	MustExec(t, se, "create table t1 (a int)")
	MustExec(t, se, "GRANT select (a), update (a),insert(a), references(a) on t1 to issue28531")
}

func TestAnalyzeVersionUpgradeFrom300To500(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// Upgrade from 3.0.0 to 5.1+ or above.
	ver300 := 33
	seV3 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver300))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV3, ver300)
	MustExec(t, seV3, fmt.Sprintf("delete from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBAnalyzeVersion))
	MustExec(t, seV3, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV3)
	require.NoError(t, err)
	require.Equal(t, int64(ver300), ver)

	// We are now in 3.0.0, check tidb_analyze_version should not exist.
	res := MustExecToRecodeSet(t, seV3, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBAnalyzeVersion))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 0, chk.NumRows())
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in version no lower than 5.x, tidb_enable_index_merge should be 1.
	res = MustExecToRecodeSet(t, seCurVer, "select @@tidb_analyze_version")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, "1", row.GetString(0))
}

func TestIndexMergeInNewCluster(t *testing.T) {
	store, err := mockstore.NewMockStore(mockstore.WithStoreType(mockstore.EmbedUnistore))
	require.NoError(t, err)
	// Indicates we are in a new cluster.
	require.Equal(t, int64(notBootstrapped), getStoreBootstrapVersionWithCache(store))
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	defer dom.Close()
	se := CreateSessionAndSetID(t, store)

	// In a new created cluster(above 5.4+), tidb_enable_index_merge is 1 by default.
	MustExec(t, se, "use test;")
	r := MustExecToRecodeSet(t, se, "select @@tidb_enable_index_merge;")
	require.NotNil(t, r)

	ctx := context.Background()
	chk := r.NewChunk(nil)
	err = r.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, int64(1), row.GetInt64(0))
}

func TestIndexMergeUpgradeFrom300To540(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// Upgrade from 3.0.0 to 5.4+.
	ver300 := 33
	seV3 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver300))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV3, ver300)
	MustExec(t, seV3, fmt.Sprintf("delete from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBEnableIndexMerge))
	MustExec(t, seV3, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV3)
	require.NoError(t, err)
	require.Equal(t, int64(ver300), ver)

	// We are now in 3.0.0, check tidb_enable_index_merge should not exist.
	res := MustExecToRecodeSet(t, seV3, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBEnableIndexMerge))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 0, chk.NumRows())
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in 5.x, tidb_enable_index_merge should be off.
	res = MustExecToRecodeSet(t, seCurVer, "select @@tidb_enable_index_merge")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, int64(0), row.GetInt64(0))
}

// We set tidb_enable_index_merge as on.
// And after upgrade to 5.x, tidb_enable_index_merge should remains to be on.
func TestIndexMergeUpgradeFrom400To540Enable(t *testing.T) {
	testIndexMergeUpgradeFrom400To540(t, true)
}

func TestIndexMergeUpgradeFrom400To540Disable(t *testing.T) {
	testIndexMergeUpgradeFrom400To540(t, false)
}

func testIndexMergeUpgradeFrom400To540(t *testing.T, enable bool) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// upgrade from 4.0.0 to 5.4+.
	ver400 := 46
	seV4 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver400))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV4, ver400)
	MustExec(t, seV4, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", vardef.Off, vardef.TiDBEnableIndexMerge))
	MustExec(t, seV4, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV4)
	require.NoError(t, err)
	require.Equal(t, int64(ver400), ver)

	// We are now in 4.0.0, tidb_enable_index_merge is off.
	res := MustExecToRecodeSet(t, seV4, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBEnableIndexMerge))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, vardef.Off, row.GetString(1))

	if enable {
		// For the first time, We set tidb_enable_index_merge as on.
		// And after upgrade to 5.x, tidb_enable_index_merge should remains to be on.
		// For the second it should be off.
		MustExec(t, seV4, "set global tidb_enable_index_merge = on")
	}
	dom.Close()
	// Upgrade to 5.x.
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in 5.x, tidb_enable_index_merge should be on because we enable it in 4.0.0.
	res = MustExecToRecodeSet(t, seCurVer, "select @@tidb_enable_index_merge")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	if enable {
		require.Equal(t, int64(1), row.GetInt64(0))
	} else {
		require.Equal(t, int64(0), row.GetInt64(0))
	}
}

func TestTiDBEnablePagingVariable(t *testing.T) {
	store, dom := CreateStoreAndBootstrap(t)
	se := CreateSessionAndSetID(t, store)
	defer func() { require.NoError(t, store.Close()) }()
	defer dom.Close()

	for _, sql := range []string{
		"select @@global.tidb_enable_paging",
		"select @@session.tidb_enable_paging",
	} {
		r := MustExecToRecodeSet(t, se, sql)
		require.NotNil(t, r)

		req := r.NewChunk(nil)
		err := r.Next(context.Background(), req)
		require.NoError(t, err)
		require.NotEqual(t, 0, req.NumRows())

		rows := statistics.RowToDatums(req.GetRow(0), r.Fields())
		if vardef.DefTiDBEnablePaging {
			match(t, rows, "1")
		} else {
			match(t, rows, "0")
		}
		r.Close()
	}
}

func TestTiDBOptRangeMaxSizeWhenUpgrading(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// Upgrade from v6.3.0 to v6.4.0+.
	ver94 := 94
	seV630 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver94))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV630, ver94)
	MustExec(t, seV630, fmt.Sprintf("delete from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBOptRangeMaxSize))
	MustExec(t, seV630, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV630)
	require.NoError(t, err)
	require.Equal(t, int64(ver94), ver)

	// We are now in 6.3.0, check tidb_opt_range_max_size should not exist.
	res := MustExecToRecodeSet(t, seV630, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBOptRangeMaxSize))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 0, chk.NumRows())
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in version no lower than v6.4.0, tidb_opt_range_max_size should be 0.
	res = MustExecToRecodeSet(t, seCurVer, "select @@session.tidb_opt_range_max_size")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, "0", row.GetString(0))

	res = MustExecToRecodeSet(t, seCurVer, "select @@global.tidb_opt_range_max_size")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, "0", row.GetString(0))
}

func TestTiDBOptAdvancedJoinHintWhenUpgrading(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// Upgrade from v6.6.0 to v7.0.0+.
	ver134 := 134
	seV660 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver134))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV660, ver134)
	MustExec(t, seV660, fmt.Sprintf("delete from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBOptAdvancedJoinHint))
	MustExec(t, seV660, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV660)
	require.NoError(t, err)
	require.Equal(t, int64(ver134), ver)

	// We are now in 6.6.0, check tidb_opt_advanced_join_hint should not exist.
	res := MustExecToRecodeSet(t, seV660, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBOptAdvancedJoinHint))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 0, chk.NumRows())
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in version no lower than v7.0.0, tidb_opt_advanced_join_hint should be false.
	res = MustExecToRecodeSet(t, seCurVer, "select @@session.tidb_opt_advanced_join_hint;")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, int64(0), row.GetInt64(0))

	res = MustExecToRecodeSet(t, seCurVer, "select @@global.tidb_opt_advanced_join_hint;")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, int64(0), row.GetInt64(0))
}

func TestTiDBOptAdvancedJoinHintInNewCluster(t *testing.T) {
	store, err := mockstore.NewMockStore(mockstore.WithStoreType(mockstore.EmbedUnistore))
	require.NoError(t, err)
	// Indicates we are in a new cluster.
	require.Equal(t, int64(notBootstrapped), getStoreBootstrapVersionWithCache(store))
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	defer dom.Close()
	se := CreateSessionAndSetID(t, store)

	// In a new created cluster(above 7.0+), tidb_opt_advanced_join_hint is true by default.
	MustExec(t, se, "use test;")
	r := MustExecToRecodeSet(t, se, "select @@tidb_opt_advanced_join_hint;")
	require.NotNil(t, r)

	ctx := context.Background()
	chk := r.NewChunk(nil)
	err = r.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, int64(1), row.GetInt64(0))
}

func TestTiDBCostModelInNewCluster(t *testing.T) {
	store, err := mockstore.NewMockStore(mockstore.WithStoreType(mockstore.EmbedUnistore))
	require.NoError(t, err)
	// Indicates we are in a new cluster.
	require.Equal(t, int64(notBootstrapped), getStoreBootstrapVersionWithCache(store))
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	defer dom.Close()
	se := CreateSessionAndSetID(t, store)

	// In a new created cluster(above 6.5+), tidb_cost_model_version is 2 by default.
	MustExec(t, se, "use test;")
	r := MustExecToRecodeSet(t, se, "select @@tidb_cost_model_version;")
	require.NotNil(t, r)

	ctx := context.Background()
	chk := r.NewChunk(nil)
	err = r.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, "2", row.GetString(0))
}

func TestTiDBCostModelUpgradeFrom300To650(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// Upgrade from 3.0.0 to 6.5+.
	ver300 := 33
	seV3 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver300))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV3, ver300)
	MustExec(t, seV3, fmt.Sprintf("delete from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBCostModelVersion))
	MustExec(t, seV3, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV3)
	require.NoError(t, err)
	require.Equal(t, int64(ver300), ver)

	// We are now in 3.0.0, check TiDBCostModelVersion should not exist.
	res := MustExecToRecodeSet(t, seV3, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBCostModelVersion))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 0, chk.NumRows())

	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in 6.5+, TiDBCostModelVersion should be 1.
	res = MustExecToRecodeSet(t, seCurVer, "select @@tidb_cost_model_version")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, "1", row.GetString(0))
}

func TestTiDBCostModelUpgradeFrom610To650(t *testing.T) {
	for i := range 2 {
		func() {
			ctx := context.Background()
			store, dom := CreateStoreAndBootstrap(t)
			defer func() { require.NoError(t, store.Close()) }()

			// upgrade from 6.1 to 6.5+.
			ver61 := 91
			seV61 := CreateSessionAndSetID(t, store)
			txn, err := store.Begin()
			require.NoError(t, err)
			m := meta.NewMutator(txn)
			err = m.FinishBootstrap(int64(ver61))
			require.NoError(t, err)
			err = txn.Commit(context.Background())
			require.NoError(t, err)
			revertVersionAndVariables(t, seV61, ver61)
			MustExec(t, seV61, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "1", vardef.TiDBCostModelVersion))
			MustExec(t, seV61, "commit")
			store.SetOption(StoreBootstrappedKey, nil)
			ver, err := getBootstrapVersion(seV61)
			require.NoError(t, err)
			require.Equal(t, int64(ver61), ver)

			// We are now in 6.1, tidb_cost_model_version is 1.
			res := MustExecToRecodeSet(t, seV61, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBCostModelVersion))
			chk := res.NewChunk(nil)
			err = res.Next(ctx, chk)
			require.NoError(t, err)
			require.Equal(t, 1, chk.NumRows())
			row := chk.GetRow(0)
			require.Equal(t, 2, row.Len())
			require.Equal(t, "1", row.GetString(1))
			res.Close()

			if i == 0 {
				// For the first time, We set tidb_cost_model_version to 2.
				// And after upgrade to 6.5, tidb_cost_model_version should be 2.
				// For the second it should be 1.
				MustExec(t, seV61, "set global tidb_cost_model_version = 2")
			}
			dom.Close()
			// Upgrade to 6.5.
			domCurVer, err := BootstrapSession(store)
			require.NoError(t, err)
			defer domCurVer.Close()
			seCurVer := CreateSessionAndSetID(t, store)
			ver, err = getBootstrapVersion(seCurVer)
			require.NoError(t, err)
			require.Equal(t, currentBootstrapVersion, ver)

			// We are now in 6.5.
			res = MustExecToRecodeSet(t, seCurVer, "select @@tidb_cost_model_version")
			chk = res.NewChunk(nil)
			err = res.Next(ctx, chk)
			require.NoError(t, err)
			require.Equal(t, 1, chk.NumRows())
			row = chk.GetRow(0)
			require.Equal(t, 1, row.Len())
			if i == 0 {
				require.Equal(t, "2", row.GetString(0))
			} else {
				require.Equal(t, "1", row.GetString(0))
			}
			res.Close()
		}()
	}
}

func TestTiDBGCAwareUpgradeFrom630To650(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// upgrade from 6.3 to 6.5+.
	ver63 := version93
	seV63 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver63))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV63, ver63)
	MustExec(t, seV63, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "1", vardef.TiDBEnableGCAwareMemoryTrack))
	MustExec(t, seV63, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV63)
	require.NoError(t, err)
	require.Equal(t, int64(ver63), ver)

	// We are now in 6.3, tidb_enable_gc_aware_memory_track is ON.
	res := MustExecToRecodeSet(t, seV63, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBEnableGCAwareMemoryTrack))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "1", row.GetString(1))

	// Upgrade to 6.5.
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in 6.5.
	res = MustExecToRecodeSet(t, seCurVer, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBEnableGCAwareMemoryTrack))
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "0", row.GetString(1))
}

func TestTiDBServerMemoryLimitUpgradeTo651_1(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// upgrade from 6.5.0 to 6.5.1+.
	ver132 := version132
	seV132 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver132))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV132, ver132)
	MustExec(t, seV132, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "0", vardef.TiDBServerMemoryLimit))
	MustExec(t, seV132, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV132)
	require.NoError(t, err)
	require.Equal(t, int64(ver132), ver)

	// We are now in 6.5.0, tidb_server_memory_limit is 0.
	res := MustExecToRecodeSet(t, seV132, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBServerMemoryLimit))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "0", row.GetString(1))

	// Upgrade to 6.5.1+.
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in 6.5.1+.
	res = MustExecToRecodeSet(t, seCurVer, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBServerMemoryLimit))
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, vardef.DefTiDBServerMemoryLimit, row.GetString(1))
}

func TestTiDBServerMemoryLimitUpgradeTo651_2(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// upgrade from 6.5.0 to 6.5.1+.
	ver132 := version132
	seV132 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver132))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV132, ver132)
	MustExec(t, seV132, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "70%", vardef.TiDBServerMemoryLimit))
	MustExec(t, seV132, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV132)
	require.NoError(t, err)
	require.Equal(t, int64(ver132), ver)

	// We are now in 6.5.0, tidb_server_memory_limit is "70%".
	res := MustExecToRecodeSet(t, seV132, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBServerMemoryLimit))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "70%", row.GetString(1))

	// Upgrade to 6.5.1+.
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in 6.5.1+.
	res = MustExecToRecodeSet(t, seCurVer, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBServerMemoryLimit))
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "70%", row.GetString(1))
}

func TestTiDBGlobalVariablesDefaultValueUpgradeFrom630To660(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// upgrade from 6.3.0 to 6.6.0.
	ver630 := version93
	seV630 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver630))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV630, ver630)
	MustExec(t, seV630, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "OFF", vardef.TiDBEnableForeignKey))
	MustExec(t, seV630, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "OFF", vardef.ForeignKeyChecks))
	MustExec(t, seV630, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "OFF", vardef.TiDBEnableHistoricalStats))
	MustExec(t, seV630, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "OFF", vardef.TiDBEnablePlanReplayerCapture))
	MustExec(t, seV630, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV630)
	require.NoError(t, err)
	require.Equal(t, int64(ver630), ver)

	// We are now in 6.3.0.
	upgradeVars := []string{vardef.TiDBEnableForeignKey, vardef.ForeignKeyChecks, vardef.TiDBEnableHistoricalStats, vardef.TiDBEnablePlanReplayerCapture}
	varsValueList := []string{"OFF", "OFF", "OFF", "OFF"}
	for i := range upgradeVars {
		res := MustExecToRecodeSet(t, seV630, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", upgradeVars[i]))
		chk := res.NewChunk(nil)
		err = res.Next(ctx, chk)
		require.NoError(t, err)
		require.Equal(t, 1, chk.NumRows())
		row := chk.GetRow(0)
		require.Equal(t, 2, row.Len())
		require.Equal(t, varsValueList[i], row.GetString(1))
	}

	// Upgrade to 6.6.0.
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seV660 := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seV660)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in 6.6.0.
	varsValueList = []string{"ON", "ON", "ON", "ON"}
	for i := range upgradeVars {
		res := MustExecToRecodeSet(t, seV660, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", upgradeVars[i]))
		chk := res.NewChunk(nil)
		err = res.Next(ctx, chk)
		require.NoError(t, err)
		require.Equal(t, 1, chk.NumRows())
		row := chk.GetRow(0)
		require.Equal(t, 2, row.Len())
		require.Equal(t, varsValueList[i], row.GetString(1))
	}
}

func TestTiDBStoreBatchSizeUpgradeFrom650To660(t *testing.T) {
	for i := range 2 {
		func() {
			ctx := context.Background()
			store, dom := CreateStoreAndBootstrap(t)
			defer func() { require.NoError(t, store.Close()) }()

			// upgrade from 6.5 to 6.6.
			ver65 := version132
			seV65 := CreateSessionAndSetID(t, store)
			txn, err := store.Begin()
			require.NoError(t, err)
			m := meta.NewMutator(txn)
			err = m.FinishBootstrap(int64(ver65))
			require.NoError(t, err)
			err = txn.Commit(context.Background())
			require.NoError(t, err)
			revertVersionAndVariables(t, seV65, ver65)
			MustExec(t, seV65, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "0", vardef.TiDBStoreBatchSize))
			MustExec(t, seV65, "commit")
			store.SetOption(StoreBootstrappedKey, nil)
			ver, err := getBootstrapVersion(seV65)
			require.NoError(t, err)
			require.Equal(t, int64(ver65), ver)

			// We are now in 6.5, tidb_store_batch_size is 0.
			res := MustExecToRecodeSet(t, seV65, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBStoreBatchSize))
			chk := res.NewChunk(nil)
			err = res.Next(ctx, chk)
			require.NoError(t, err)
			require.Equal(t, 1, chk.NumRows())
			row := chk.GetRow(0)
			require.Equal(t, 2, row.Len())
			require.Equal(t, "0", row.GetString(1))
			res.Close()

			if i == 0 {
				// For the first time, We set tidb_store_batch_size to 1.
				// And after upgrade to 6.6, tidb_store_batch_size should be 1.
				// For the second it should be the latest default value.
				MustExec(t, seV65, "set global tidb_store_batch_size = 1")
			}
			dom.Close()
			// Upgrade to 6.6.
			domCurVer, err := BootstrapSession(store)
			require.NoError(t, err)
			defer domCurVer.Close()
			seCurVer := CreateSessionAndSetID(t, store)
			ver, err = getBootstrapVersion(seCurVer)
			require.NoError(t, err)
			require.Equal(t, currentBootstrapVersion, ver)

			// We are now in 6.6.
			res = MustExecToRecodeSet(t, seCurVer, "select @@tidb_store_batch_size")
			chk = res.NewChunk(nil)
			err = res.Next(ctx, chk)
			require.NoError(t, err)
			require.Equal(t, 1, chk.NumRows())
			row = chk.GetRow(0)
			require.Equal(t, 1, row.Len())
			if i == 0 {
				require.Equal(t, "1", row.GetString(0))
			} else {
				require.Equal(t, "4", row.GetString(0))
			}
			res.Close()
		}()
	}
}

func TestTiDBUpgradeToVer136(t *testing.T) {
	store, do := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()

	ver135 := version135
	seV135 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver135))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV135, ver135)
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV135)
	require.NoError(t, err)
	require.Equal(t, int64(ver135), ver)

	MustExec(t, seV135, "ALTER TABLE mysql.tidb_background_subtask DROP INDEX idx_task_key;")
	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/reorgMetaRecordFastReorgDisabled", `return`))
	t.Cleanup(func() {
		require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/reorgMetaRecordFastReorgDisabled"))
	})
	MustExec(t, seV135, "set global tidb_ddl_enable_fast_reorg = 1")
	do.Close()
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	ver, err = getBootstrapVersion(seV135)
	require.NoError(t, err)
	require.True(t, ddl.LastReorgMetaFastReorgDisabled)

	require.Less(t, int64(ver135), ver)
	dom.Close()
}

func TestTiDBUpgradeToVer140(t *testing.T) {
	store, do := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()

	ver139 := version139
	resetTo139 := func(s sessionapi.Session) {
		txn, err := store.Begin()
		require.NoError(t, err)
		m := meta.NewMutator(txn)
		err = m.FinishBootstrap(int64(ver139))
		require.NoError(t, err)
		revertVersionAndVariables(t, s, ver139)
		err = txn.Commit(context.Background())
		require.NoError(t, err)

		store.SetOption(StoreBootstrappedKey, nil)
		ver, err := getBootstrapVersion(s)
		require.NoError(t, err)
		require.Equal(t, int64(ver139), ver)
	}

	// drop column task_key and then upgrade
	s := CreateSessionAndSetID(t, store)
	MustExec(t, s, "alter table mysql.tidb_global_task drop column task_key")
	resetTo139(s)
	do.Close()
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	ver, err := getBootstrapVersion(s)
	require.NoError(t, err)
	require.Less(t, int64(ver139), ver)
	dom.Close()

	// upgrade with column task_key exists
	s = CreateSessionAndSetID(t, store)
	resetTo139(s)
	dom, err = BootstrapSession(store)
	require.NoError(t, err)
	ver, err = getBootstrapVersion(s)
	require.NoError(t, err)
	require.Less(t, int64(ver139), ver)
	dom.Close()
}

func TestTiDBNonPrepPlanCacheUpgradeFrom540To700(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// bootstrap to 5.4
	ver54 := version82
	seV54 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver54))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV54, ver54)
	MustExec(t, seV54, fmt.Sprintf("delete from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBEnableNonPreparedPlanCache))
	MustExec(t, seV54, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV54)
	require.NoError(t, err)
	require.Equal(t, int64(ver54), ver)

	// We are now in 5.4, check TiDBCostModelVersion should not exist.
	res := MustExecToRecodeSet(t, seV54, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBEnableNonPreparedPlanCache))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 0, chk.NumRows())

	// Upgrade to 7.0
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in 7.0
	res = MustExecToRecodeSet(t, seCurVer, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBEnableNonPreparedPlanCache))
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "OFF", row.GetString(1)) // tidb_enable_non_prepared_plan_cache = off

	res = MustExecToRecodeSet(t, seCurVer, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBNonPreparedPlanCacheSize))
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "100", row.GetString(1)) // tidb_non_prepared_plan_cache_size = 100
}

func TestTiDBStatsLoadPseudoTimeoutUpgradeFrom610To650(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// upgrade from 6.1 to 6.5+.
	ver61 := version91
	seV61 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver61))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV61, ver61)
	MustExec(t, seV61, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "0", vardef.TiDBStatsLoadPseudoTimeout))
	MustExec(t, seV61, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV61)
	require.NoError(t, err)
	require.Equal(t, int64(ver61), ver)

	// We are now in 6.1, tidb_stats_load_pseudo_timeout is OFF.
	res := MustExecToRecodeSet(t, seV61, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBStatsLoadPseudoTimeout))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "0", row.GetString(1))

	// Upgrade to 6.5.
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in 6.5.
	res = MustExecToRecodeSet(t, seCurVer, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBStatsLoadPseudoTimeout))
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "1", row.GetString(1))
}

func TestTiDBTiDBOptTiDBOptimizerEnableNAAJWhenUpgradingToVer138(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	ver137 := version137
	seV137 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver137))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV137, ver137)
	MustExec(t, seV137, "update mysql.GLOBAL_VARIABLES set variable_value='OFF' where variable_name='tidb_enable_null_aware_anti_join'")
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV137)
	require.NoError(t, err)
	require.Equal(t, int64(ver137), ver)

	res := MustExecToRecodeSet(t, seV137, "select * from mysql.GLOBAL_VARIABLES where variable_name='tidb_enable_null_aware_anti_join'")
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "OFF", row.GetString(1))

	// Upgrade to version 138.
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	res = MustExecToRecodeSet(t, seCurVer, "select * from mysql.GLOBAL_VARIABLES where variable_name='tidb_enable_null_aware_anti_join'")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "ON", row.GetString(1))
}

func TestTiDBUpgradeToVer143(t *testing.T) {
	store, do := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()

	ver142 := version142
	seV142 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver142))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV142, ver142)
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV142)
	require.NoError(t, err)
	require.Equal(t, int64(ver142), ver)

	do.Close()
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	ver, err = getBootstrapVersion(seV142)
	require.NoError(t, err)
	require.Less(t, int64(ver142), ver)
	dom.Close()
}

func TestTiDBLoadBasedReplicaReadThresholdUpgradingToVer141(t *testing.T) {
	ctx := context.Background()
	store, do := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// upgrade from 7.0 to 7.1.
	ver70 := version139
	seV70 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver70))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV70, ver70)
	MustExec(t, seV70, fmt.Sprintf("update mysql.GLOBAL_VARIABLES set variable_value='%s' where variable_name='%s'", "0", vardef.TiDBLoadBasedReplicaReadThreshold))
	MustExec(t, seV70, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV70)
	require.NoError(t, err)
	require.Equal(t, int64(ver70), ver)

	// We are now in 7.0, tidb_load_based_replica_read_threshold is 0.
	res := MustExecToRecodeSet(t, seV70, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBLoadBasedReplicaReadThreshold))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "0", row.GetString(1))

	// Upgrade to 7.1.
	do.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in 7.1.
	res = MustExecToRecodeSet(t, seCurVer, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBLoadBasedReplicaReadThreshold))
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, 2, row.Len())
	require.Equal(t, "1s", row.GetString(1))
}

func TestTiDBPlanCacheInvalidationOnFreshStatsWhenUpgradingToVer144(t *testing.T) {
	ctx := context.Background()
	store, do := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// bootstrap as version143
	ver143 := version143
	seV143 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver143))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV143, ver143)
	// simulate a real ver143 where `tidb_plan_cache_invalidation_on_fresh_stats` doesn't exist yet
	MustExec(t, seV143, "delete from mysql.GLOBAL_VARIABLES where variable_name='tidb_plan_cache_invalidation_on_fresh_stats'")
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	store.SetOption(StoreBootstrappedKey, nil)

	// upgrade to ver144
	do.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err := getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// the value in the table is set to OFF automatically
	res := MustExecToRecodeSet(t, seCurVer, "select * from mysql.GLOBAL_VARIABLES where variable_name='tidb_plan_cache_invalidation_on_fresh_stats'")
	chk := res.NewChunk(nil)
	require.NoError(t, res.Next(ctx, chk))
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, "OFF", row.GetString(1))

	// the session and global variable is also OFF
	res = MustExecToRecodeSet(t, seCurVer, "select @@session.tidb_plan_cache_invalidation_on_fresh_stats, @@global.tidb_plan_cache_invalidation_on_fresh_stats")
	chk = res.NewChunk(nil)
	require.NoError(t, res.Next(ctx, chk))
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, int64(0), row.GetInt64(0))
	require.Equal(t, int64(0), row.GetInt64(1))
}

func TestTiDBUpgradeToVer145(t *testing.T) {
	store, do := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()

	ver144 := version144
	seV144 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver144))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV144, ver144)
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV144)
	require.NoError(t, err)
	require.Equal(t, int64(ver144), ver)

	do.Close()
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	ver, err = getBootstrapVersion(seV144)
	require.NoError(t, err)
	require.Less(t, int64(ver144), ver)
	dom.Close()
}

func TestTiDBUpgradeToVer170(t *testing.T) {
	store, do := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()
	ver169 := version169
	seV169 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver169))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV169, ver169)
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV169)
	require.NoError(t, err)
	require.Equal(t, int64(ver169), ver)

	do.Close()
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	ver, err = getBootstrapVersion(seV169)
	require.NoError(t, err)
	require.Less(t, int64(ver169), ver)
	dom.Close()
}

func TestTiDBUpgradeToVer176(t *testing.T) {
	store, do := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()
	ver175 := version175
	seV175 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver175))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV175, ver175)
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV175)
	require.NoError(t, err)
	require.Equal(t, int64(ver175), ver)

	do.Close()
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	ver, err = getBootstrapVersion(seV175)
	require.NoError(t, err)
	require.Less(t, int64(ver175), ver)
	MustExec(t, seV175, "SELECT * from mysql.tidb_global_task_history")
	dom.Close()
}

func TestTiDBUpgradeToVer177(t *testing.T) {
	store, do := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()
	ver176 := version176
	seV176 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver176))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV176, ver176)
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV176)
	require.NoError(t, err)
	require.Equal(t, int64(ver176), ver)

	do.Close()
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	ver, err = getBootstrapVersion(seV176)
	require.NoError(t, err)
	require.Less(t, int64(ver176), ver)
	MustExec(t, seV176, "SELECT * from mysql.dist_framework_meta")
	dom.Close()
}

func TestWriteDDLTableVersionToMySQLTiDB(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	ddlTableVer, err := m.GetDDLTableVersion()
	require.NoError(t, err)

	// Verify that 'ddl_table_version' has been set to the correct value
	se := CreateSessionAndSetID(t, store)
	r := MustExecToRecodeSet(t, se, fmt.Sprintf(`SELECT VARIABLE_VALUE from mysql.TiDB where VARIABLE_NAME='%s'`, tidbDDLTableVersion))
	req := r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, req.NumRows())
	require.Equal(t, fmt.Appendf(nil, "%d", ddlTableVer), req.GetRow(0).GetBytes(0))
	require.NoError(t, r.Close())
	dom.Close()
}

func TestWriteDDLTableVersionToMySQLTiDBWhenUpgradingTo178(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	ddlTableVer, err := m.GetDDLTableVersion()
	require.NoError(t, err)

	// bootstrap as version177
	ver177 := version177
	seV177 := CreateSessionAndSetID(t, store)
	err = m.FinishBootstrap(int64(ver177))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV177, ver177)
	// remove the ddl_table_version entry from mysql.tidb table
	MustExec(t, seV177, fmt.Sprintf("delete from mysql.tidb where VARIABLE_NAME='%s'", tidbDDLTableVersion))
	err = txn.Commit(ctx)
	require.NoError(t, err)
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV177)
	require.NoError(t, err)
	require.Equal(t, int64(ver177), ver)

	// upgrade to current version
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// check if the DDLTableVersion has been set in the `mysql.tidb` table during upgrade
	r := MustExecToRecodeSet(t, seCurVer, fmt.Sprintf(`SELECT VARIABLE_VALUE from mysql.TiDB where VARIABLE_NAME='%s'`, tidbDDLTableVersion))
	req := r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, req.NumRows())
	require.Equal(t, fmt.Appendf(nil, "%d", ddlTableVer), req.GetRow(0).GetBytes(0))
	require.NoError(t, r.Close())
}

func TestTiDBUpgradeToVer179(t *testing.T) {
	ctx := context.Background()
	store, do := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()
	ver178 := version178
	seV178 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver178))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV178, ver178)
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV178)
	require.NoError(t, err)
	require.Equal(t, int64(ver178), ver)

	do.Close()
	dom, err := BootstrapSession(store)
	require.NoError(t, err)
	ver, err = getBootstrapVersion(seV178)
	require.NoError(t, err)
	require.Less(t, int64(ver178), ver)

	r := MustExecToRecodeSet(t, seV178, "desc mysql.global_variables")
	req := r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 2, req.NumRows())
	require.Equal(t, []byte("varchar(16383)"), req.GetRow(1).GetBytes(1))
	require.NoError(t, r.Close())

	dom.Close()
}

func testTiDBUpgradeWithDistTask(t *testing.T, injectQuery string, fatal bool) {
	store, do := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()
	ver178 := version178
	seV178 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver178))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV178, ver178)
	MustExec(t, seV178, injectQuery)
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV178)
	require.NoError(t, err)
	require.Equal(t, int64(ver178), ver)

	conf := new(log.Config)
	lg, p, e := log.InitLogger(conf, zap.WithFatalHook(zapcore.WriteThenPanic))
	require.NoError(t, e)
	rs := log.ReplaceGlobals(lg, p)
	defer func() {
		rs()
	}()

	do.Close()
	fatal2panic := false
	fc := func() {
		defer func() {
			if err := recover(); err != nil {
				fatal2panic = true
			}
		}()
		_, _ = BootstrapSession(store)
	}
	fc()
	var dom *domain.Domain
	dom, err = domap.Get(store)
	require.NoError(t, err)
	dom.Close()
	require.Equal(t, fatal, fatal2panic)
}

func TestTiDBUpgradeToVer209(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// bootstrap as version198, version 199~208 is reserved for v8.1.x bugfix patch.
	ver198 := version198
	seV198 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver198))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV198, ver198)
	// simulate a real ver198 where `tidb_resource_control_strict_mode` doesn't exist yet
	MustExec(t, seV198, "delete from mysql.GLOBAL_VARIABLES where variable_name='tidb_resource_control_strict_mode'")
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	store.SetOption(StoreBootstrappedKey, nil)

	// upgrade to ver209
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err := getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// the value in the table is set to OFF automatically
	res := MustExecToRecodeSet(t, seCurVer, "select * from mysql.GLOBAL_VARIABLES where variable_name='tidb_resource_control_strict_mode'")
	chk := res.NewChunk(nil)
	require.NoError(t, res.Next(ctx, chk))
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, "OFF", row.GetString(1))

	// the global variable is also OFF
	res = MustExecToRecodeSet(t, seCurVer, "select @@global.tidb_resource_control_strict_mode")
	chk = res.NewChunk(nil)
	require.NoError(t, res.Next(ctx, chk))
	require.Equal(t, 1, chk.NumRows())
	row = chk.GetRow(0)
	require.Equal(t, int64(0), row.GetInt64(0))
	require.Equal(t, false, vardef.EnableResourceControlStrictMode.Load())
}

func TestTiDBUpgradeWithDistTaskEnable(t *testing.T) {
	t.Run("test enable dist task", func(t *testing.T) { testTiDBUpgradeWithDistTask(t, "set global tidb_enable_dist_task = 1", false) })
	t.Run("test disable dist task", func(t *testing.T) { testTiDBUpgradeWithDistTask(t, "set global tidb_enable_dist_task = 0", false) })
}

func TestTiDBUpgradeWithDistTaskRunning(t *testing.T) {
	t.Run("test dist task running", func(t *testing.T) {
		testTiDBUpgradeWithDistTask(t, "insert into mysql.tidb_global_task set id = 1, task_key = 'aaa', type= 'aaa', state = 'running'", false)
	})
	t.Run("test dist task succeed", func(t *testing.T) {
		testTiDBUpgradeWithDistTask(t, "insert into mysql.tidb_global_task set id = 1, task_key = 'aaa', type= 'aaa', state = 'succeed'", false)
	})
	t.Run("test dist task failed", func(t *testing.T) {
		testTiDBUpgradeWithDistTask(t, "insert into mysql.tidb_global_task set id = 1, task_key = 'aaa', type= 'aaa', state = 'failed'", false)
	})
	t.Run("test dist task reverted", func(t *testing.T) {
		testTiDBUpgradeWithDistTask(t, "insert into mysql.tidb_global_task set id = 1, task_key = 'aaa', type= 'aaa', state = 'reverted'", false)
	})
	t.Run("test dist task paused", func(t *testing.T) {
		testTiDBUpgradeWithDistTask(t, "insert into mysql.tidb_global_task set id = 1, task_key = 'aaa', type= 'aaa', state = 'paused'", false)
	})
	t.Run("test dist task other", func(t *testing.T) {
		testTiDBUpgradeWithDistTask(t, "insert into mysql.tidb_global_task set id = 1, task_key = 'aaa', type= 'aaa', state = 'other'", false)
	})
}

func TestTiDBUpgradeToVer211(t *testing.T) {
	ctx := context.Background()
	store, do := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()
	ver210 := version210
	seV210 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver210))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV210, ver210)
	err = txn.Commit(context.Background())
	require.NoError(t, err)

	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV210)
	require.NoError(t, err)
	require.Equal(t, int64(ver210), ver)
	MustExec(t, seV210, "alter table mysql.tidb_background_subtask_history drop column summary;")

	do.Close()
	dom, err := BootstrapSession(store)
	require.NoError(t, err)

	newSe := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(newSe)
	require.NoError(t, err)
	require.Less(t, int64(ver210), ver)

	r := MustExecToRecodeSet(t, newSe, "select count(summary) from mysql.tidb_background_subtask_history;")
	req := r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, req.NumRows())
	require.NoError(t, r.Close())

	dom.Close()
}

func TestTiDBHistoryTableConsistent(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() {
		require.NoError(t, store.Close())
	}()

	se := CreateSessionAndSetID(t, store)
	query := `select (select group_concat(column_name) from information_schema.columns where table_name='tidb_background_subtask' order by ordinal_position)
	               = (select group_concat(column_name) from information_schema.columns where table_name='tidb_background_subtask_history' order by ordinal_position);`
	r := MustExecToRecodeSet(t, se, query)
	req := r.NewChunk(nil)
	err := r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, req.NumRows())
	row := req.GetRow(0)
	require.Equal(t, int64(1), row.GetInt64(0))

	query = `select (select group_concat(column_name) from information_schema.columns where table_name='tidb_global_task' order by ordinal_position)
	              = (select group_concat(column_name) from information_schema.columns where table_name='tidb_global_task_history' order by ordinal_position);`
	r = MustExecToRecodeSet(t, se, query)
	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, req.NumRows())
	row = req.GetRow(0)
	require.Equal(t, int64(1), row.GetInt64(0))

	dom.Close()
}

func TestTiDBUpgradeToVer212(t *testing.T) {
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// bootstrap as version198, version 199~208 is reserved for v8.1.x bugfix patch.
	ver198 := version198
	seV198 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver198))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV198, ver198)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	store.SetOption(StoreBootstrappedKey, nil)

	// upgrade to ver212
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err := getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)
	// the columns are changed automatically
	MustExec(t, seCurVer, "select sample_sql, start_time, plan_digest from mysql.tidb_runaway_queries")
}

func TestIssue61890(t *testing.T) {
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	s1 := CreateSessionAndSetID(t, store)
	MustExec(t, s1, "drop table mysql.global_variables")
	MustExec(t, s1, "create table mysql.global_variables(`VARIABLE_NAME` varchar(64) NOT NULL PRIMARY KEY clustered, `VARIABLE_VALUE` varchar(16383) DEFAULT NULL)")

	s2 := CreateSessionAndSetID(t, store)
	initGlobalVariableIfNotExists(s2, vardef.TiDBEnableINLJoinInnerMultiPattern, vardef.Off)

	dom.Close()
}

func TestIndexJoinMultiPatternByUpgrade650To840(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// Upgrade from 6.5.0 to 8.4+ or above.
	ver650 := 109
	seV7 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver650))
	require.NoError(t, err)
	err = txn.Commit(context.Background())
	require.NoError(t, err)
	revertVersionAndVariables(t, seV7, ver650)
	MustExec(t, seV7, fmt.Sprintf("delete from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBEnableINLJoinInnerMultiPattern))
	MustExec(t, seV7, "commit")
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV7)
	require.NoError(t, err)
	require.Equal(t, int64(ver650), ver)

	// We are now in 6.5.0, check tidb_enable_inl_join_inner_multi_pattern should not exist.
	res := MustExecToRecodeSet(t, seV7, fmt.Sprintf("select * from mysql.GLOBAL_VARIABLES where variable_name='%s'", vardef.TiDBEnableINLJoinInnerMultiPattern))
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 0, chk.NumRows())
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// We are now in version no lower than 8.4, tidb_enable_inl_join_inner_multi_pattern be off.
	res = MustExecToRecodeSet(t, seCurVer, "select @@global.tidb_enable_inl_join_inner_multi_pattern")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	row := chk.GetRow(0)
	require.Equal(t, 1, row.Len())
	require.Equal(t, int64(0), row.GetInt64(0))
}

func TestKeyspaceEtcdNamespace(t *testing.T) {
	keyspaceMeta := keyspacepb.KeyspaceMeta{}
	keyspaceMeta.Id = 2
	keyspaceMeta.Name = "test_ks_name2"
	makeStore(t, &keyspaceMeta, true)
}

func TestNullKeyspaceEtcdNamespace(t *testing.T) {
	makeStore(t, nil, false)
}

func makeStore(t *testing.T, keyspaceMeta *keyspacepb.KeyspaceMeta, isHasPrefix bool) {
	integration.BeforeTestExternal(t)
	var store kv.Storage
	var err error
	if keyspaceMeta != nil {
		store, err = mockstore.NewMockStore(
			mockstore.WithKeyspaceMeta(keyspaceMeta),
			mockstore.WithStoreType(mockstore.EmbedUnistore),
		)
	} else {
		store, err = mockstore.NewMockStore(mockstore.WithStoreType(mockstore.EmbedUnistore))
	}
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	cluster := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 1})
	defer cluster.Terminate(t)

	// Build a mockEtcdBackend.
	mockStore := &mockEtcdBackend{
		Storage: store,
		pdAddrs: []string{cluster.Members[0].GRPCURL()}}
	etcdClient := cluster.RandClient()

	require.NoError(t, err)
	dom, err := domap.getWithEtcdClient(mockStore, etcdClient)
	require.NoError(t, err)
	defer dom.Close()

	checkETCDNameSpace(t, dom, isHasPrefix)
}

func checkETCDNameSpace(t *testing.T, dom *domain.Domain, isHasPrefix bool) {
	namespacePrefix := keyspace.MakeKeyspaceEtcdNamespace(dom.Store().GetCodec())
	testKeyWithoutPrefix := "/testkey"
	testVal := "test"
	var expectTestKey string
	if isHasPrefix {
		expectTestKey = namespacePrefix + testKeyWithoutPrefix
	} else {
		expectTestKey = testKeyWithoutPrefix
	}

	// Put key value into etcd.
	_, err := dom.EtcdClient().Put(context.Background(), testKeyWithoutPrefix, testVal)
	require.NoError(t, err)

	// Use expectTestKey to get the key from etcd.
	getResp, err := dom.UnprefixedEtcdCli().Get(context.Background(), expectTestKey)
	require.NoError(t, err)
	require.Equal(t, len(getResp.Kvs), 1)

	if isHasPrefix {
		getResp, err = dom.UnprefixedEtcdCli().Get(context.Background(), testKeyWithoutPrefix)
		require.NoError(t, err)
		require.Equal(t, 0, len(getResp.Kvs))
	}
}

type mockEtcdBackend struct {
	kv.Storage
	pdAddrs []string
}

func (mebd *mockEtcdBackend) EtcdAddrs() ([]string, error) {
	return mebd.pdAddrs, nil
}

func (mebd *mockEtcdBackend) TLSConfig() *tls.Config { return nil }

func (mebd *mockEtcdBackend) StartGCWorker() error { return nil }

func TestTiDBUpgradeToVer240(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	ver239 := version239
	seV239 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver239))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV239, ver239)
	err = txn.Commit(ctx)
	require.NoError(t, err)
	store.SetOption(StoreBootstrappedKey, nil)

	// Check if the required indexes already exist in mysql.analyze_jobs (they are created by default in new clusters)
	res := MustExecToRecodeSet(t, seV239, "show create table mysql.analyze_jobs")
	chk := res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	require.Contains(t, string(chk.GetRow(0).GetBytes(1)), "idx_schema_table_state")
	require.Contains(t, string(chk.GetRow(0).GetBytes(1)), "idx_schema_table_partition_state")

	// Check that the indexes still exist after upgrading to the new version and that no errors occurred during the upgrade.
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err := getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	res = MustExecToRecodeSet(t, seCurVer, "show create table mysql.analyze_jobs")
	chk = res.NewChunk(nil)
	err = res.Next(ctx, chk)
	require.NoError(t, err)
	require.Equal(t, 1, chk.NumRows())
	require.Contains(t, string(chk.GetRow(0).GetBytes(1)), "idx_schema_table_state")
	require.Contains(t, string(chk.GetRow(0).GetBytes(1)), "idx_schema_table_partition_state")
}

func TestWriteClusterIDToMySQLTiDBWhenUpgradingTo242(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// `cluster_id` is inserted for a new TiDB cluster.
	se := CreateSessionAndSetID(t, store)
	r := MustExecToRecodeSet(t, se, `select VARIABLE_VALUE from mysql.tidb where VARIABLE_NAME='cluster_id'`)
	req := r.NewChunk(nil)
	err := r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, req.NumRows())
	require.NotEmpty(t, req.GetRow(0).GetBytes(0))
	require.NoError(t, r.Close())
	se.Close()

	// bootstrap as version241
	ver241 := version241
	seV241 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver241))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV241, ver241)
	// remove the cluster_id entry from mysql.tidb table
	MustExec(t, seV241, "delete from mysql.tidb where variable_name='cluster_id'")
	err = txn.Commit(ctx)
	require.NoError(t, err)
	store.SetOption(StoreBootstrappedKey, nil)
	ver, err := getBootstrapVersion(seV241)
	require.NoError(t, err)
	require.Equal(t, int64(ver241), ver)
	seV241.Close()

	// upgrade to current version
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err = getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)

	// check if the cluster_id has been set in the `mysql.tidb` table during upgrade
	r = MustExecToRecodeSet(t, seCurVer, `select VARIABLE_VALUE from mysql.tidb where VARIABLE_NAME='cluster_id'`)
	req = r.NewChunk(nil)
	err = r.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, req.NumRows())
	require.NotEmpty(t, req.GetRow(0).GetBytes(0))
	require.NoError(t, r.Close())
	seCurVer.Close()
}

func TestBindInfoUniqueIndex(t *testing.T) {
	ctx := context.Background()
	store, dom := CreateStoreAndBootstrap(t)
	defer func() { require.NoError(t, store.Close()) }()

	// bootstrap as version245
	ver245 := version245
	seV245 := CreateSessionAndSetID(t, store)
	txn, err := store.Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	err = m.FinishBootstrap(int64(ver245))
	require.NoError(t, err)
	revertVersionAndVariables(t, seV245, ver245)
	err = txn.Commit(ctx)
	require.NoError(t, err)
	store.SetOption(StoreBootstrappedKey, nil)

	// remove the unique index on mysql.bind_info for testing
	MustExec(t, seV245, "alter table mysql.bind_info drop index digest_index")

	// insert duplicated values into mysql.bind_info
	for _, sqlDigest := range []string{"null", "'x'", "'y'"} {
		for _, planDigest := range []string{"null", "'x'", "'y'"} {
			insertStmt := fmt.Sprintf(`insert into mysql.bind_info values (
             "sql", "bind_sql", "db", "disabled", NOW(), NOW(), "", "", "", %s, %s)`,
				sqlDigest, planDigest)
			MustExec(t, seV245, insertStmt)
			MustExec(t, seV245, insertStmt)
		}
	}

	// upgrade to current version
	dom.Close()
	domCurVer, err := BootstrapSession(store)
	require.NoError(t, err)
	defer domCurVer.Close()
	seCurVer := CreateSessionAndSetID(t, store)
	ver, err := getBootstrapVersion(seCurVer)
	require.NoError(t, err)
	require.Equal(t, currentBootstrapVersion, ver)
}

func TestVersionedBootstrapSchemas(t *testing.T) {
	require.True(t, slices.IsSortedFunc(versionedBootstrapSchemas, func(a, b versionedBootstrapSchema) int {
		return cmp.Compare(a.ver, b.ver)
	}), "versionedBootstrapSchemas should be sorted by version")

	// make sure that later change won't affect existing version schemas.
	require.Len(t, versionedBootstrapSchemas[0].databases[0].Tables, 52)
	require.Len(t, versionedBootstrapSchemas[0].databases[1].Tables, 0)

	allIDs := make([]int64, 0, len(versionedBootstrapSchemas))
	var allTableCount int
	for _, vbs := range versionedBootstrapSchemas {
		for _, db := range vbs.databases {
			require.Greater(t, db.ID, metadef.ReservedGlobalIDLowerBound)
			require.LessOrEqual(t, db.ID, metadef.ReservedGlobalIDUpperBound)
			allIDs = append(allIDs, db.ID)

			testTableBasicInfoSlice(t, db.Tables)
			allTableCount += len(db.Tables)
			for _, tbl := range db.Tables {
				allIDs = append(allIDs, tbl.ID)
			}
		}
	}
	require.Len(t, tablesInSystemDatabase, allTableCount,
		"versionedBootstrapSchemas should have the same number of tables as tablesInSystemDatabase")
	slices.Sort(allIDs)
	require.IsIncreasing(t, allIDs, "versionedBootstrapSchemas should not have duplicate IDs")
}
