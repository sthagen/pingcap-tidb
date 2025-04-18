// Copyright 2023 PingCAP, Inc.
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

package adminpause

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	testddlutil "github.com/pingcap/tidb/pkg/ddl/testutil"
	"github.com/pingcap/tidb/pkg/domain"
	"github.com/pingcap/tidb/pkg/errno"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/pingcap/tidb/pkg/util/sqlexec"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func pauseResumeAndCancel(t *testing.T, stmtKit *testkit.TestKit, adminCommandKit *testkit.TestKit, dom *domain.Domain, stmtCase *StmtCase, doCancel bool) {
	Logger.Info("pauseResumeAndCancel: case start,",
		zap.Int("Global ID", stmtCase.globalID),
		zap.String("Statement", stmtCase.stmt),
		zap.String("Schema state", stmtCase.schemaState.String()),
		zap.Strings("Pre-condition", stmtCase.preConditionStmts),
		zap.Strings("Rollback statement", stmtCase.rollbackStmts),
		zap.Bool("Job pausable", stmtCase.isJobPausable))

	var jobID int64 = 0
	var adminCommandMutex sync.Mutex

	var isPaused = false
	var pauseResult []sqlexec.RecordSet
	var pauseErr error
	var pauseFunc = func(job *model.Job) {
		Logger.Debug("pauseResumeAndCancel: OnJobRunBeforeExported, ",
			zap.String("Job Type", job.Type.String()),
			zap.String("Job State", job.State.String()),
			zap.String("Job Schema State", job.SchemaState.String()),
			zap.String("Expected Schema State", stmtCase.schemaState.String()))

		// All hooks are running inside a READ mutex `d.mu()`, we need to lock for the variable's modification.
		adminCommandMutex.Lock()
		defer adminCommandMutex.Unlock()
		if testddlutil.MatchCancelState(t, job, stmtCase.schemaState, stmtCase.stmt) &&
			stmtCase.isJobPausable &&
			!isPaused {
			jobID = job.ID
			stmt := "admin pause ddl jobs " + strconv.FormatInt(jobID, 10)
			pauseResult, pauseErr = adminCommandKit.Session().Execute(context.Background(), stmt)

			Logger.Info("pauseResumeAndCancel: pause command by hook.OnJobRunBeforeExported,",
				zap.Error(pauseErr), zap.String("Statement", stmtCase.stmt), zap.Int64("Job ID", jobID))

			// In case that it runs into this scope again and again
			isPaused = true
		}
	}
	var verifyPauseResult = func(t *testing.T, adminCommandKit *testkit.TestKit) {
		adminCommandMutex.Lock()
		defer adminCommandMutex.Unlock()

		require.True(t, isPaused)
		require.NoError(t, pauseErr)
		result := adminCommandKit.ResultSetToResultWithCtx(context.Background(),
			pauseResult[0], "pause ddl job successfully")
		result.Check(testkit.Rows(fmt.Sprintf("%d successful", jobID)))

		isPaused = false
	}

	var isResumed = false
	var resumeResult []sqlexec.RecordSet
	var resumeErr error
	var resumeFunc = func(job *model.Job) {
		Logger.Debug("pauseResumeAndCancel: OnJobUpdatedExported, ",
			zap.String("Job Type", job.Type.String()),
			zap.String("Job State", job.State.String()),
			zap.String("Job Schema State", job.SchemaState.String()),
			zap.String("Expected Schema State", stmtCase.schemaState.String()))

		adminCommandMutex.Lock()
		defer adminCommandMutex.Unlock()
		if job.IsPaused() && isPaused && !isResumed {
			stmt := "admin resume ddl jobs " + strconv.FormatInt(jobID, 10)
			resumeResult, resumeErr = adminCommandKit.Session().Execute(context.Background(), stmt)

			Logger.Info("pauseResumeAndCancel: resume command by hook.OnJobUpdatedExported: ",
				zap.Error(resumeErr), zap.String("Statement", stmtCase.stmt), zap.Int64("Job ID", jobID))

			// In case that it runs into this scope again and again
			isResumed = true
		}
	}
	var verifyResumeResult = func(t *testing.T, adminCommandKit *testkit.TestKit) {
		adminCommandMutex.Lock()
		defer adminCommandMutex.Unlock()

		require.True(t, isResumed)
		require.NoError(t, resumeErr)
		result := adminCommandKit.ResultSetToResultWithCtx(context.Background(),
			resumeResult[0], "resume ddl job successfully")
		result.Check(testkit.Rows(fmt.Sprintf("%d successful", jobID)))

		isResumed = false
	}

	var isCancelled = false
	var cancelResult []sqlexec.RecordSet
	var cancelErr error
	var cancelFunc = func() {
		adminCommandMutex.Lock()
		defer adminCommandMutex.Unlock()
		if isPaused && isResumed && !isCancelled {
			stmt := "admin cancel ddl jobs " + strconv.FormatInt(jobID, 10)
			cancelResult, cancelErr = adminCommandKit.Session().Execute(context.Background(), stmt)

			Logger.Info("pauseResumeAndCancel: cancel command by hook.OnGetJobBeforeExported: ",
				zap.Error(cancelErr), zap.String("Statement", stmtCase.stmt), zap.Int64("Job ID", jobID))

			// In case that it runs into this scope again and again
			isCancelled = true
		}
	}
	var verifyCancelResult = func(t *testing.T, adminCommandKit *testkit.TestKit) {
		adminCommandMutex.Lock()
		defer adminCommandMutex.Unlock()

		require.True(t, isCancelled)
		require.NoError(t, cancelErr)
		result := adminCommandKit.ResultSetToResultWithCtx(context.Background(),
			cancelResult[0], "resume ddl job successfully")
		result.Check(testkit.Rows(fmt.Sprintf("%d successful", jobID)))

		isCancelled = false
	}

	for _, prepareStmt := range stmtCase.preConditionStmts {
		Logger.Debug("pauseResumeAndCancel: ", zap.String("Prepare schema before test", prepareStmt))
		stmtKit.MustExec(prepareStmt)
	}

	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/afterWaitSchemaSynced", resumeFunc)

	Logger.Debug("pauseResumeAndCancel: statement execute", zap.String("DDL Statement", stmtCase.stmt))
	if stmtCase.isJobPausable {
		if doCancel {
			testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/beforeLoadAndDeliverJobs", func() {
				cancelFunc()
			})
			testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/beforeRunOneJobStep", pauseFunc)

			stmtKit.MustGetErrCode(stmtCase.stmt, errno.ErrCancelledDDLJob)
			testfailpoint.Disable(t, "github.com/pingcap/tidb/pkg/ddl/beforeLoadAndDeliverJobs")
			Logger.Info("pauseResumeAndCancel: statement execution should have been cancelled.")

			verifyCancelResult(t, adminCommandKit)
		} else {
			testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/beforeRunOneJobStep", pauseFunc)

			stmtKit.MustExec(stmtCase.stmt)
			Logger.Info("pauseResumeAndCancel: statement execution should finish successfully.")

			// If not `doCancel`, then we should not run the `admin cancel`, which the indication should be false.
			require.False(t, isCancelled)
		}

		// The reason why we don't check the result of `admin pause`, `admin resume` and `admin cancel` inside the
		// hook is that the hook may not be triggered, which we may not know it.
		verifyPauseResult(t, adminCommandKit)
		verifyResumeResult(t, adminCommandKit)
	} else {
		testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/beforeRunOneJobStep", pauseFunc)
		stmtKit.MustExec(stmtCase.stmt)

		require.False(t, isPaused)
		require.False(t, isResumed)
		require.False(t, isCancelled)
	}

	// Should not affect the 'stmtCase.rollbackStmts'
	testfailpoint.Disable(t, "github.com/pingcap/tidb/pkg/ddl/beforeRunOneJobStep")
	testfailpoint.Disable(t, "github.com/pingcap/tidb/pkg/ddl/afterWaitSchemaSynced")

	// Statement in `stmtCase` will be finished successfully all the way, need to roll it back.
	for _, rollbackStmt := range stmtCase.rollbackStmts {
		Logger.Debug("pauseResumeAndCancel: ", zap.String("Rollback Statement", rollbackStmt))
		_, _ = stmtKit.Exec(rollbackStmt)
	}

	Logger.Info("pauseResumeAndCancel: statement case finished, ",
		zap.String("Global ID", strconv.Itoa(stmtCase.globalID)))
}

func TestPauseAndResumeSchemaStmt(t *testing.T) {
	var dom, stmtKit, adminCommandKit = prepareDomain(t)

	require.Nil(t, generateTblUser(stmtKit, 10))

	for _, stmtCase := range schemaDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, false)
	}
	for _, stmtCase := range tableDDLStmt {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, false)
	}

	for _, stmtCase := range placeRulDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, false)
	}

	Logger.Info("TestPauseAndResumeSchemaStmt: all cases finished.")
}

func TestPauseAndResumeIndexStmt(t *testing.T) {
	var dom, stmtKit, adminCommandKit = prepareDomain(t)

	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/infoschema/mockTiFlashStoreCount", `return(true)`)
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/MockCheckColumnarIndexProcess", `return(1)`)

	require.Nil(t, generateTblUser(stmtKit, 10))
	require.Nil(t, generateTblUserWithVec(stmtKit, 10))

	for _, stmtCase := range indexDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, false)
	}

	Logger.Info("TestPauseAndResumeIndexStmt: all cases finished.")
}

func TestPauseAndResumeColumnStmt(t *testing.T) {
	var dom, stmtKit, adminCommandKit = prepareDomain(t)

	require.Nil(t, generateTblUser(stmtKit, 10))

	for _, stmtCase := range columnDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, false)
	}

	Logger.Info("TestPauseAndResumeColumnStmt: all cases finished.")
}

func TestPauseAndResumePartitionTableStmt(t *testing.T) {
	var dom, stmtKit, adminCommandKit = prepareDomain(t)

	require.Nil(t, generateTblUser(stmtKit, 0))

	require.Nil(t, generateTblUserParition(stmtKit))
	for _, stmtCase := range tablePartitionDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, false)
	}

	Logger.Info("TestPauseAndResumePartitionTableStmt: all cases finished.")
}

func TestPauseResumeCancelAndRerunSchemaStmt(t *testing.T) {
	var dom, stmtKit, adminCommandKit = prepareDomain(t)

	require.Nil(t, generateTblUser(stmtKit, 10))

	for _, stmtCase := range schemaDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, true)

		Logger.Info("TestPauseResumeCancelAndRerunSchemaStmt: statement execution again after `admin cancel`",
			zap.String("DDL Statement", stmtCase.stmt))
		stmtCase.simpleRunStmt(stmtKit)
	}
	for _, stmtCase := range tableDDLStmt {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, true)

		Logger.Info("TestPauseResumeCancelAndRerunSchemaStmt: statement execution again after `admin cancel`",
			zap.String("DDL Statement", stmtCase.stmt))
		stmtCase.simpleRunStmt(stmtKit)
	}

	for _, stmtCase := range placeRulDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, true)

		Logger.Info("TestPauseResumeCancelAndRerunSchemaStmt: statement execution again after `admin cancel`",
			zap.String("DDL Statement", stmtCase.stmt))
		stmtCase.simpleRunStmt(stmtKit)
	}
	Logger.Info("TestPauseResumeCancelAndRerunSchemaStmt: all cases finished.")
}

func TestPauseResumeCancelAndRerunIndexStmt(t *testing.T) {
	var dom, stmtKit, adminCommandKit = prepareDomain(t)

	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/infoschema/mockTiFlashStoreCount", `return(true)`)
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/MockCheckColumnarIndexProcess", `return(1)`)

	require.Nil(t, generateTblUser(stmtKit, 10))
	require.Nil(t, generateTblUserWithVec(stmtKit, 10))

	for _, stmtCase := range indexDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, true)

		Logger.Info("TestPauseResumeCancelAndRerunIndexStmt: statement execution again after `admin cancel`",
			zap.String("DDL Statement", stmtCase.stmt))
		stmtCase.simpleRunStmt(stmtKit)
	}
	Logger.Info("TestPauseResumeCancelAndRerunIndexStmt: all cases finished.")
}

func TestPauseResumeCancelAndRerunColumnStmt(t *testing.T) {
	var dom, stmtKit, adminCommandKit = prepareDomain(t)

	require.Nil(t, generateTblUser(stmtKit, 10))

	for _, stmtCase := range columnDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, true)

		Logger.Info("TestPauseResumeCancelAndRerunColumnStmt: statement execution again after `admin cancel`",
			zap.String("DDL Statement", stmtCase.stmt))
		stmtCase.simpleRunStmt(stmtKit)
	}

	// It will be out of control if the tuples generated is not in the range of some cases for partition. Then it would
	// fail. Just truncate the tuples here because we don't care about the partition itself but the DDL.
	stmtKit.MustExec("truncate " + adminPauseTestTable)

	require.Nil(t, generateTblUserParition(stmtKit))
	for _, stmtCase := range tablePartitionDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, true)

		Logger.Info("TestPauseResumeCancelAndRerunColumnStmt: statement execution again after `admin cancel`",
			zap.String("DDL Statement", stmtCase.stmt))
		stmtCase.simpleRunStmt(stmtKit)
	}

	Logger.Info("TestPauseResumeCancelAndRerunColumnStmt: all cases finished.")
}

func TestPauseResumeCancelAndRerunPartitionTableStmt(t *testing.T) {
	var dom, stmtKit, adminCommandKit = prepareDomain(t)

	require.Nil(t, generateTblUser(stmtKit, 0))

	require.Nil(t, generateTblUserParition(stmtKit))
	for _, stmtCase := range tablePartitionDDLStmtCase {
		pauseResumeAndCancel(t, stmtKit, adminCommandKit, dom, &stmtCase, true)

		Logger.Info("TestPauseResumeCancelAndRerunPartitionTableStmt: statement execution again after `admin cancel`",
			zap.String("DDL Statement", stmtCase.stmt))
		stmtCase.simpleRunStmt(stmtKit)
	}

	Logger.Info("TestPauseResumeCancelAndRerunPartitionTableStmt: all cases finished.")
}

func TestPauseJobDependency(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk2 := testkit.NewTestKit(t, store)
	tk2.MustExec("use test")

	tk.MustExec("create table t (a int, b int);")
	tk.MustExec("insert into t values (1, 1);")

	afterPause := make(chan struct{})
	afterAddCol := make(chan struct{})
	startAddCol := make(chan struct{})
	var (
		modifyJobID int64
		errModCol   error
		errAddCol   error
	)
	once := sync.Once{}
	failpoint.EnableCall("github.com/pingcap/tidb/pkg/ddl/afterModifyColumnStateDeleteOnly", func(jobID int64) {
		once.Do(func() {
			modifyJobID = jobID
			tk2.MustExec(fmt.Sprintf("admin pause ddl jobs %d", jobID))
			afterPause <- struct{}{}
		})
	})
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Will stuck because the job is paused.
		errModCol = tk.ExecToErr("alter table t modify column b tinyint;")
	}()
	go func() {
		defer wg.Done()
		<-afterPause
		// This should be blocked because they handle the same table.
		startAddCol <- struct{}{}
		errAddCol = tk2.ExecToErr("alter table t add column c int;")
		afterAddCol <- struct{}{}
	}()
	<-startAddCol
	select {
	case <-afterAddCol:
		t.Logf("add column DDL on same table should be blocked")
		t.FailNow()
	case <-time.After(3 * time.Second):
		tk3 := testkit.NewTestKit(t, store)
		tk3.MustExec("use test")
		tk3.MustExec(fmt.Sprintf("admin resume ddl jobs %d", modifyJobID))
		<-afterAddCol
	}
	wg.Wait()
	require.NoError(t, errModCol)
	require.NoError(t, errAddCol)
}
