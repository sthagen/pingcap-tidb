// Copyright 2019 PingCAP, Inc.
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

package stmtsummary

import (
	"bytes"
	"container/list"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/auth"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/sessionctx/stmtctx"
	"github.com/pingcap/tidb/pkg/types"
	tidbutil "github.com/pingcap/tidb/pkg/util"
	"github.com/pingcap/tidb/pkg/util/execdetails"
	"github.com/pingcap/tidb/pkg/util/hack"
	"github.com/pingcap/tidb/pkg/util/plancodec"
	"github.com/pingcap/tidb/pkg/util/ppcpuusage"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/util"
)

func emptyPlanGenerator() (string, string, any) {
	return "", "", nil
}

func fakePlanDigestGenerator() string {
	return "point_get"
}

func TestSetUp(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	err := ssMap.SetEnabled(true)
	require.NoError(t, err)
	err = ssMap.SetRefreshInterval(1800)
	require.NoError(t, err)
	err = ssMap.SetHistorySize(24)
	require.NoError(t, err)
}

const (
	boTxnLockName = "txnlock"
)

// Test stmtSummaryByDigest.AddStatement.
func TestAddStatement(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()
	ssMap.beginTimeForCurInterval = now + 60

	tables := []stmtctx.TableEntry{{DB: "db1", Table: "tb1"}, {DB: "db2", Table: "tb2"}}
	indexes := []string{"a", "b"}

	sc := stmtctx.NewStmtCtx()
	sc.StmtType = "Select"
	sc.Tables = tables
	sc.IndexNames = indexes

	// first statement
	stmtExecInfo1 := generateAnyExecInfo()
	stmtExecInfo1.ExecDetail.CommitDetail.Mu.PrewriteBackoffTypes = make([]string, 0)
	key := &StmtDigestKey{}
	key.Init(stmtExecInfo1.SchemaName, stmtExecInfo1.Digest, "", stmtExecInfo1.PlanDigest, stmtExecInfo1.ResourceGroupName)
	samplePlan, _, _ := stmtExecInfo1.LazyInfo.GetEncodedPlan()
	stmtExecInfo1.ExecDetail.CommitDetail.Mu.Lock()
	expectedSummaryElement := stmtSummaryByDigestElement{
		beginTime: now + 60,
		endTime:   now + 1860,
		stmtSummaryStats: stmtSummaryStats{
			sampleSQL:            stmtExecInfo1.LazyInfo.GetOriginalSQL(),
			samplePlan:           samplePlan,
			indexNames:           stmtExecInfo1.StmtCtx.IndexNames,
			execCount:            1,
			sumLatency:           stmtExecInfo1.TotalLatency,
			maxLatency:           stmtExecInfo1.TotalLatency,
			minLatency:           stmtExecInfo1.TotalLatency,
			sumParseLatency:      stmtExecInfo1.ParseLatency,
			maxParseLatency:      stmtExecInfo1.ParseLatency,
			sumCompileLatency:    stmtExecInfo1.CompileLatency,
			maxCompileLatency:    stmtExecInfo1.CompileLatency,
			sumNumCopTasks:       int64(stmtExecInfo1.CopTasks.NumCopTasks),
			sumCopProcessTime:    stmtExecInfo1.CopTasks.TotProcessTime,
			maxCopProcessTime:    stmtExecInfo1.CopTasks.MaxProcessTime,
			maxCopProcessAddress: stmtExecInfo1.CopTasks.MaxProcessAddress,
			sumCopWaitTime:       stmtExecInfo1.CopTasks.TotWaitTime,
			maxCopWaitTime:       stmtExecInfo1.CopTasks.MaxWaitTime,
			maxCopWaitAddress:    stmtExecInfo1.CopTasks.MaxWaitAddress,
			sumProcessTime:       stmtExecInfo1.ExecDetail.TimeDetail.ProcessTime,
			maxProcessTime:       stmtExecInfo1.ExecDetail.TimeDetail.ProcessTime,
			sumWaitTime:          stmtExecInfo1.ExecDetail.TimeDetail.WaitTime,
			maxWaitTime:          stmtExecInfo1.ExecDetail.TimeDetail.WaitTime,
			sumBackoffTime:       stmtExecInfo1.ExecDetail.BackoffTime,
			maxBackoffTime:       stmtExecInfo1.ExecDetail.BackoffTime,
			sumTotalKeys:         stmtExecInfo1.ExecDetail.ScanDetail.TotalKeys,
			maxTotalKeys:         stmtExecInfo1.ExecDetail.ScanDetail.TotalKeys,
			sumProcessedKeys:     stmtExecInfo1.ExecDetail.ScanDetail.ProcessedKeys,
			maxProcessedKeys:     stmtExecInfo1.ExecDetail.ScanDetail.ProcessedKeys,
			sumGetCommitTsTime:   stmtExecInfo1.ExecDetail.CommitDetail.GetCommitTsTime,
			maxGetCommitTsTime:   stmtExecInfo1.ExecDetail.CommitDetail.GetCommitTsTime,
			sumPrewriteTime:      stmtExecInfo1.ExecDetail.CommitDetail.PrewriteTime,
			maxPrewriteTime:      stmtExecInfo1.ExecDetail.CommitDetail.PrewriteTime,
			sumCommitTime:        stmtExecInfo1.ExecDetail.CommitDetail.CommitTime,
			maxCommitTime:        stmtExecInfo1.ExecDetail.CommitDetail.CommitTime,
			sumLocalLatchTime:    stmtExecInfo1.ExecDetail.CommitDetail.LocalLatchTime,
			maxLocalLatchTime:    stmtExecInfo1.ExecDetail.CommitDetail.LocalLatchTime,
			sumCommitBackoffTime: stmtExecInfo1.ExecDetail.CommitDetail.Mu.CommitBackoffTime,
			maxCommitBackoffTime: stmtExecInfo1.ExecDetail.CommitDetail.Mu.CommitBackoffTime,
			sumResolveLockTime:   stmtExecInfo1.ExecDetail.CommitDetail.ResolveLock.ResolveLockTime,
			maxResolveLockTime:   stmtExecInfo1.ExecDetail.CommitDetail.ResolveLock.ResolveLockTime,
			sumWriteKeys:         int64(stmtExecInfo1.ExecDetail.CommitDetail.WriteKeys),
			maxWriteKeys:         stmtExecInfo1.ExecDetail.CommitDetail.WriteKeys,
			sumWriteSize:         int64(stmtExecInfo1.ExecDetail.CommitDetail.WriteSize),
			maxWriteSize:         stmtExecInfo1.ExecDetail.CommitDetail.WriteSize,
			sumPrewriteRegionNum: int64(stmtExecInfo1.ExecDetail.CommitDetail.PrewriteRegionNum),
			maxPrewriteRegionNum: stmtExecInfo1.ExecDetail.CommitDetail.PrewriteRegionNum,
			sumTxnRetry:          int64(stmtExecInfo1.ExecDetail.CommitDetail.TxnRetry),
			maxTxnRetry:          stmtExecInfo1.ExecDetail.CommitDetail.TxnRetry,
			backoffTypes:         make(map[string]int),
			sumMem:               stmtExecInfo1.MemMax,
			maxMem:               stmtExecInfo1.MemMax,
			sumDisk:              stmtExecInfo1.DiskMax,
			maxDisk:              stmtExecInfo1.DiskMax,
			sumAffectedRows:      stmtExecInfo1.StmtCtx.AffectedRows(),
			firstSeen:            stmtExecInfo1.StartTime,
			lastSeen:             stmtExecInfo1.StartTime,
			StmtRUSummary: StmtRUSummary{
				SumRRU:            stmtExecInfo1.RUDetail.RRU(),
				MaxRRU:            stmtExecInfo1.RUDetail.RRU(),
				SumWRU:            stmtExecInfo1.RUDetail.WRU(),
				MaxWRU:            stmtExecInfo1.RUDetail.WRU(),
				SumRUWaitDuration: stmtExecInfo1.RUDetail.RUWaitDuration(),
				MaxRUWaitDuration: stmtExecInfo1.RUDetail.RUWaitDuration(),
			},
			resourceGroupName: stmtExecInfo1.ResourceGroupName,
			StmtNetworkTrafficSummary: StmtNetworkTrafficSummary{
				UnpackedBytesSentTiKVTotal:            stmtExecInfo1.TiKVExecDetails.UnpackedBytesSentKVTotal,
				UnpackedBytesReceivedTiKVTotal:        stmtExecInfo1.TiKVExecDetails.UnpackedBytesReceivedKVTotal,
				UnpackedBytesSentTiKVCrossZone:        stmtExecInfo1.TiKVExecDetails.UnpackedBytesSentKVCrossZone,
				UnpackedBytesReceivedTiKVCrossZone:    stmtExecInfo1.TiKVExecDetails.UnpackedBytesReceivedKVCrossZone,
				UnpackedBytesSentTiFlashTotal:         stmtExecInfo1.TiKVExecDetails.UnpackedBytesSentMPPTotal,
				UnpackedBytesReceivedTiFlashTotal:     stmtExecInfo1.TiKVExecDetails.UnpackedBytesReceivedMPPTotal,
				UnpackedBytesSentTiFlashCrossZone:     stmtExecInfo1.TiKVExecDetails.UnpackedBytesSentMPPCrossZone,
				UnpackedBytesReceivedTiFlashCrossZone: stmtExecInfo1.TiKVExecDetails.UnpackedBytesReceivedMPPCrossZone,
			},
			storageKV:  stmtExecInfo1.StmtCtx.IsTiKV.Load(),
			storageMPP: stmtExecInfo1.StmtCtx.IsTiFlash.Load(),
		},
	}
	stmtExecInfo1.ExecDetail.CommitDetail.Mu.Unlock()
	history := list.New()
	history.PushBack(&expectedSummaryElement)
	expectedSummary := stmtSummaryByDigest{
		schemaName:    stmtExecInfo1.SchemaName,
		stmtType:      stmtExecInfo1.StmtCtx.StmtType,
		digest:        stmtExecInfo1.Digest,
		normalizedSQL: stmtExecInfo1.NormalizedSQL,
		planDigest:    stmtExecInfo1.PlanDigest,
		tableNames:    "db1.tb1,db2.tb2",
		history:       history,
	}
	ssMap.AddStatement(stmtExecInfo1)
	summary, ok := ssMap.summaryMap.Get(key)
	require.True(t, ok)
	require.True(t, matchStmtSummaryByDigest(summary.(*stmtSummaryByDigest), &expectedSummary))

	// Second statement is similar with the first statement, and its values are
	// greater than that of the first statement.
	stmtExecInfo2 := &StmtExecInfo{
		SchemaName:     "schema_name",
		NormalizedSQL:  "normalized_sql",
		Digest:         "digest",
		PlanDigest:     "plan_digest",
		User:           "user2",
		TotalLatency:   20000,
		ParseLatency:   200,
		CompileLatency: 2000,
		CopTasks: &execdetails.CopTasksSummary{
			NumCopTasks:       20,
			MaxProcessAddress: "200",
			MaxProcessTime:    25000,
			TotProcessTime:    40000,
			MaxWaitAddress:    "201",
			MaxWaitTime:       2500,
			TotWaitTime:       40000,
		},
		ExecDetail: execdetails.ExecDetails{
			BackoffTime:  180,
			RequestCount: 20,
			CommitDetail: &util.CommitDetails{
				GetCommitTsTime: 500,
				PrewriteTime:    50000,
				CommitTime:      5000,
				LocalLatchTime:  50,
				Mu: struct {
					sync.Mutex
					CommitBackoffTime    int64
					PrewriteBackoffTypes []string
					CommitBackoffTypes   []string
					SlowestPrewrite      util.ReqDetailInfo
					CommitPrimary        util.ReqDetailInfo
				}{
					CommitBackoffTime:    1000,
					PrewriteBackoffTypes: []string{boTxnLockName},
					CommitBackoffTypes:   []string{},
					SlowestPrewrite:      util.ReqDetailInfo{},
					CommitPrimary:        util.ReqDetailInfo{},
				},
				WriteKeys:         100000,
				WriteSize:         1000000,
				PrewriteRegionNum: 100,
				TxnRetry:          10,
				ResolveLock: util.ResolveLockDetail{
					ResolveLockTime: 10000,
				},
			},
			ScanDetail: &util.ScanDetail{
				TotalKeys:                 6000,
				ProcessedKeys:             1500,
				RocksdbDeleteSkippedCount: 100,
				RocksdbKeySkippedCount:    10,
				RocksdbBlockCacheHitCount: 10,
				RocksdbBlockReadCount:     10,
				RocksdbBlockReadByte:      1000,
			},
			DetailsNeedP90: execdetails.DetailsNeedP90{
				TimeDetail: util.TimeDetail{
					ProcessTime: 1500,
					WaitTime:    150,
				}, CalleeAddress: "202",
			},
		},
		StmtCtx:   sc,
		MemMax:    20000,
		DiskMax:   20000,
		StartTime: time.Date(2019, 1, 1, 10, 10, 20, 10, time.UTC),
		Succeed:   true,
		RUDetail:  util.NewRUDetailsWith(123.0, 45.6, 2*time.Second),
		TiKVExecDetails: &util.ExecDetails{
			TrafficDetails: util.TrafficDetails{
				UnpackedBytesSentKVTotal:     100,
				UnpackedBytesReceivedKVTotal: 200,
			},
		},
		ResourceGroupName: "rg1",
		LazyInfo: &mockLazyInfo{
			originalSQL:   "original_sql2",
			plan:          "",
			hintStr:       "",
			binPlan:       "",
			planDigest:    "",
			bindingSQL:    "binding_sql2",
			bindingDigest: "binding_digest2",
		},
	}
	stmtExecInfo2.StmtCtx.AddAffectedRows(200)
	expectedSummaryElement.execCount++
	expectedSummaryElement.sumLatency += stmtExecInfo2.TotalLatency
	expectedSummaryElement.maxLatency = stmtExecInfo2.TotalLatency
	expectedSummaryElement.sumParseLatency += stmtExecInfo2.ParseLatency
	expectedSummaryElement.maxParseLatency = stmtExecInfo2.ParseLatency
	expectedSummaryElement.sumCompileLatency += stmtExecInfo2.CompileLatency
	expectedSummaryElement.maxCompileLatency = stmtExecInfo2.CompileLatency
	expectedSummaryElement.sumNumCopTasks += int64(stmtExecInfo2.CopTasks.NumCopTasks)
	expectedSummaryElement.sumCopProcessTime += stmtExecInfo2.CopTasks.TotProcessTime
	expectedSummaryElement.maxCopProcessTime = stmtExecInfo2.CopTasks.MaxProcessTime
	expectedSummaryElement.maxCopProcessAddress = stmtExecInfo2.CopTasks.MaxProcessAddress
	expectedSummaryElement.sumCopWaitTime += stmtExecInfo2.CopTasks.TotWaitTime
	expectedSummaryElement.maxCopWaitTime = stmtExecInfo2.CopTasks.MaxWaitTime
	expectedSummaryElement.maxCopWaitAddress = stmtExecInfo2.CopTasks.MaxWaitAddress
	expectedSummaryElement.sumProcessTime += stmtExecInfo2.ExecDetail.TimeDetail.ProcessTime
	expectedSummaryElement.maxProcessTime = stmtExecInfo2.ExecDetail.TimeDetail.ProcessTime
	expectedSummaryElement.sumWaitTime += stmtExecInfo2.ExecDetail.TimeDetail.WaitTime
	expectedSummaryElement.maxWaitTime = stmtExecInfo2.ExecDetail.TimeDetail.WaitTime
	expectedSummaryElement.sumBackoffTime += stmtExecInfo2.ExecDetail.BackoffTime
	expectedSummaryElement.maxBackoffTime = stmtExecInfo2.ExecDetail.BackoffTime
	expectedSummaryElement.sumTotalKeys += stmtExecInfo2.ExecDetail.ScanDetail.TotalKeys
	expectedSummaryElement.maxTotalKeys = stmtExecInfo2.ExecDetail.ScanDetail.TotalKeys
	expectedSummaryElement.sumProcessedKeys += stmtExecInfo2.ExecDetail.ScanDetail.ProcessedKeys
	expectedSummaryElement.maxProcessedKeys = stmtExecInfo2.ExecDetail.ScanDetail.ProcessedKeys
	expectedSummaryElement.sumGetCommitTsTime += stmtExecInfo2.ExecDetail.CommitDetail.GetCommitTsTime
	expectedSummaryElement.maxGetCommitTsTime = stmtExecInfo2.ExecDetail.CommitDetail.GetCommitTsTime
	expectedSummaryElement.sumPrewriteTime += stmtExecInfo2.ExecDetail.CommitDetail.PrewriteTime
	expectedSummaryElement.maxPrewriteTime = stmtExecInfo2.ExecDetail.CommitDetail.PrewriteTime
	expectedSummaryElement.sumCommitTime += stmtExecInfo2.ExecDetail.CommitDetail.CommitTime
	expectedSummaryElement.maxCommitTime = stmtExecInfo2.ExecDetail.CommitDetail.CommitTime
	expectedSummaryElement.sumLocalLatchTime += stmtExecInfo2.ExecDetail.CommitDetail.LocalLatchTime
	expectedSummaryElement.maxLocalLatchTime = stmtExecInfo2.ExecDetail.CommitDetail.LocalLatchTime
	stmtExecInfo2.ExecDetail.CommitDetail.Mu.Lock()
	expectedSummaryElement.sumCommitBackoffTime += stmtExecInfo2.ExecDetail.CommitDetail.Mu.CommitBackoffTime
	expectedSummaryElement.maxCommitBackoffTime = stmtExecInfo2.ExecDetail.CommitDetail.Mu.CommitBackoffTime
	stmtExecInfo2.ExecDetail.CommitDetail.Mu.Unlock()
	expectedSummaryElement.sumResolveLockTime += stmtExecInfo2.ExecDetail.CommitDetail.ResolveLock.ResolveLockTime
	expectedSummaryElement.maxResolveLockTime = stmtExecInfo2.ExecDetail.CommitDetail.ResolveLock.ResolveLockTime
	expectedSummaryElement.sumWriteKeys += int64(stmtExecInfo2.ExecDetail.CommitDetail.WriteKeys)
	expectedSummaryElement.maxWriteKeys = stmtExecInfo2.ExecDetail.CommitDetail.WriteKeys
	expectedSummaryElement.sumWriteSize += int64(stmtExecInfo2.ExecDetail.CommitDetail.WriteSize)
	expectedSummaryElement.maxWriteSize = stmtExecInfo2.ExecDetail.CommitDetail.WriteSize
	expectedSummaryElement.sumPrewriteRegionNum += int64(stmtExecInfo2.ExecDetail.CommitDetail.PrewriteRegionNum)
	expectedSummaryElement.maxPrewriteRegionNum = stmtExecInfo2.ExecDetail.CommitDetail.PrewriteRegionNum
	expectedSummaryElement.sumTxnRetry += int64(stmtExecInfo2.ExecDetail.CommitDetail.TxnRetry)
	expectedSummaryElement.maxTxnRetry = stmtExecInfo2.ExecDetail.CommitDetail.TxnRetry
	expectedSummaryElement.sumBackoffTimes++
	expectedSummaryElement.backoffTypes[boTxnLockName] = 1
	expectedSummaryElement.sumMem += stmtExecInfo2.MemMax
	expectedSummaryElement.maxMem = stmtExecInfo2.MemMax
	expectedSummaryElement.sumDisk += stmtExecInfo2.DiskMax
	expectedSummaryElement.maxDisk = stmtExecInfo2.DiskMax
	expectedSummaryElement.sumAffectedRows += stmtExecInfo2.StmtCtx.AffectedRows()
	expectedSummaryElement.lastSeen = stmtExecInfo2.StartTime
	expectedSummaryElement.SumRRU += stmtExecInfo2.RUDetail.RRU()
	expectedSummaryElement.MaxRRU = stmtExecInfo2.RUDetail.RRU()
	expectedSummaryElement.SumWRU += stmtExecInfo2.RUDetail.WRU()
	expectedSummaryElement.MaxWRU = stmtExecInfo2.RUDetail.WRU()
	expectedSummaryElement.SumRUWaitDuration += stmtExecInfo2.RUDetail.RUWaitDuration()
	expectedSummaryElement.MaxRUWaitDuration = stmtExecInfo2.RUDetail.RUWaitDuration()
	expectedSummaryElement.StmtNetworkTrafficSummary.Add(stmtExecInfo2.TiKVExecDetails)
	expectedSummaryElement.storageKV = stmtExecInfo2.StmtCtx.IsTiKV.Load()
	expectedSummaryElement.storageMPP = stmtExecInfo2.StmtCtx.IsTiFlash.Load()

	ssMap.AddStatement(stmtExecInfo2)
	summary, ok = ssMap.summaryMap.Get(key)
	require.True(t, ok)
	require.True(t, matchStmtSummaryByDigest(summary.(*stmtSummaryByDigest), &expectedSummary))

	// Third statement is similar with the first statement, and its values are
	// less than that of the first statement.
	stmtExecInfo3 := &StmtExecInfo{
		SchemaName:     "schema_name",
		NormalizedSQL:  "normalized_sql",
		Digest:         "digest",
		PlanDigest:     "plan_digest",
		User:           "user3",
		TotalLatency:   1000,
		ParseLatency:   50,
		CompileLatency: 500,
		CopTasks: &execdetails.CopTasksSummary{
			NumCopTasks:       2,
			MaxProcessAddress: "300",
			MaxProcessTime:    350,
			TotProcessTime:    200,
			MaxWaitAddress:    "301",
			MaxWaitTime:       250,
			TotWaitTime:       40,
		},
		ExecDetail: execdetails.ExecDetails{
			BackoffTime:  18,
			RequestCount: 2,
			CommitDetail: &util.CommitDetails{
				GetCommitTsTime: 50,
				PrewriteTime:    5000,
				CommitTime:      500,
				LocalLatchTime:  5,
				Mu: struct {
					sync.Mutex
					CommitBackoffTime    int64
					PrewriteBackoffTypes []string
					CommitBackoffTypes   []string
					SlowestPrewrite      util.ReqDetailInfo
					CommitPrimary        util.ReqDetailInfo
				}{
					CommitBackoffTime:    100,
					PrewriteBackoffTypes: []string{boTxnLockName},
					CommitBackoffTypes:   []string{},
					SlowestPrewrite:      util.ReqDetailInfo{},
					CommitPrimary:        util.ReqDetailInfo{},
				},
				WriteKeys:         10000,
				WriteSize:         100000,
				PrewriteRegionNum: 10,
				TxnRetry:          1,
				ResolveLock: util.ResolveLockDetail{
					ResolveLockTime: 1000,
				},
			},
			ScanDetail: &util.ScanDetail{
				TotalKeys:                 600,
				ProcessedKeys:             150,
				RocksdbDeleteSkippedCount: 100,
				RocksdbKeySkippedCount:    10,
				RocksdbBlockCacheHitCount: 10,
				RocksdbBlockReadCount:     10,
				RocksdbBlockReadByte:      1000,
			},
			DetailsNeedP90: execdetails.DetailsNeedP90{
				TimeDetail: util.TimeDetail{
					ProcessTime: 150,
					WaitTime:    15,
				},
				CalleeAddress: "302",
			},
		},
		StmtCtx:           sc,
		MemMax:            200,
		DiskMax:           200,
		StartTime:         time.Date(2019, 1, 1, 10, 10, 0, 10, time.UTC),
		Succeed:           true,
		RUDetail:          util.NewRUDetailsWith(0.12, 0.34, 5*time.Microsecond),
		ResourceGroupName: "rg1",
		TiKVExecDetails: &util.ExecDetails{
			TrafficDetails: util.TrafficDetails{
				UnpackedBytesSentKVTotal:      1,
				UnpackedBytesReceivedKVTotal:  300,
				UnpackedBytesSentMPPTotal:     1,
				UnpackedBytesReceivedMPPTotal: 300,
			},
		},
		LazyInfo: &mockLazyInfo{
			originalSQL:   "original_sql3",
			plan:          "",
			hintStr:       "",
			binPlan:       "",
			planDigest:    "",
			bindingSQL:    "binding_sql3",
			bindingDigest: "binding_digest3",
		},
	}
	stmtExecInfo3.StmtCtx.AddAffectedRows(20000)
	expectedSummaryElement.execCount++
	expectedSummaryElement.sumLatency += stmtExecInfo3.TotalLatency
	expectedSummaryElement.minLatency = stmtExecInfo3.TotalLatency
	expectedSummaryElement.sumParseLatency += stmtExecInfo3.ParseLatency
	expectedSummaryElement.sumCompileLatency += stmtExecInfo3.CompileLatency
	expectedSummaryElement.sumNumCopTasks += int64(stmtExecInfo3.CopTasks.NumCopTasks)
	expectedSummaryElement.sumCopProcessTime += stmtExecInfo3.CopTasks.TotProcessTime
	expectedSummaryElement.sumCopWaitTime += stmtExecInfo3.CopTasks.TotWaitTime
	expectedSummaryElement.sumProcessTime += stmtExecInfo3.ExecDetail.TimeDetail.ProcessTime
	expectedSummaryElement.sumWaitTime += stmtExecInfo3.ExecDetail.TimeDetail.WaitTime
	expectedSummaryElement.sumBackoffTime += stmtExecInfo3.ExecDetail.BackoffTime
	expectedSummaryElement.sumTotalKeys += stmtExecInfo3.ExecDetail.ScanDetail.TotalKeys
	expectedSummaryElement.sumProcessedKeys += stmtExecInfo3.ExecDetail.ScanDetail.ProcessedKeys
	expectedSummaryElement.sumGetCommitTsTime += stmtExecInfo3.ExecDetail.CommitDetail.GetCommitTsTime
	expectedSummaryElement.sumPrewriteTime += stmtExecInfo3.ExecDetail.CommitDetail.PrewriteTime
	expectedSummaryElement.sumCommitTime += stmtExecInfo3.ExecDetail.CommitDetail.CommitTime
	expectedSummaryElement.sumLocalLatchTime += stmtExecInfo3.ExecDetail.CommitDetail.LocalLatchTime
	stmtExecInfo3.ExecDetail.CommitDetail.Mu.Lock()
	expectedSummaryElement.sumCommitBackoffTime += stmtExecInfo3.ExecDetail.CommitDetail.Mu.CommitBackoffTime
	stmtExecInfo3.ExecDetail.CommitDetail.Mu.Unlock()
	expectedSummaryElement.sumResolveLockTime += stmtExecInfo3.ExecDetail.CommitDetail.ResolveLock.ResolveLockTime
	expectedSummaryElement.sumWriteKeys += int64(stmtExecInfo3.ExecDetail.CommitDetail.WriteKeys)
	expectedSummaryElement.sumWriteSize += int64(stmtExecInfo3.ExecDetail.CommitDetail.WriteSize)
	expectedSummaryElement.sumPrewriteRegionNum += int64(stmtExecInfo3.ExecDetail.CommitDetail.PrewriteRegionNum)
	expectedSummaryElement.sumTxnRetry += int64(stmtExecInfo3.ExecDetail.CommitDetail.TxnRetry)
	expectedSummaryElement.sumBackoffTimes++
	expectedSummaryElement.backoffTypes[boTxnLockName] = 2
	expectedSummaryElement.sumMem += stmtExecInfo3.MemMax
	expectedSummaryElement.sumDisk += stmtExecInfo3.DiskMax
	expectedSummaryElement.sumAffectedRows += stmtExecInfo3.StmtCtx.AffectedRows()
	expectedSummaryElement.firstSeen = stmtExecInfo3.StartTime
	expectedSummaryElement.SumRRU += stmtExecInfo3.RUDetail.RRU()
	expectedSummaryElement.SumWRU += stmtExecInfo3.RUDetail.WRU()
	expectedSummaryElement.SumRUWaitDuration += stmtExecInfo3.RUDetail.RUWaitDuration()
	expectedSummaryElement.StmtNetworkTrafficSummary.Add(stmtExecInfo3.TiKVExecDetails)
	expectedSummaryElement.storageKV = stmtExecInfo3.StmtCtx.IsTiKV.Load()
	expectedSummaryElement.storageMPP = stmtExecInfo3.StmtCtx.IsTiFlash.Load()

	ssMap.AddStatement(stmtExecInfo3)
	summary, ok = ssMap.summaryMap.Get(key)
	require.True(t, ok)
	require.True(t, matchStmtSummaryByDigest(summary.(*stmtSummaryByDigest), &expectedSummary))

	// Fourth statement is in a different schema.
	stmtExecInfo4 := stmtExecInfo1
	stmtExecInfo4.SchemaName = "schema2"
	stmtExecInfo4.ExecDetail.CommitDetail = nil
	key = &StmtDigestKey{}
	key.Init(stmtExecInfo4.SchemaName, stmtExecInfo4.Digest, "", stmtExecInfo4.PlanDigest, stmtExecInfo4.ResourceGroupName)
	ssMap.AddStatement(stmtExecInfo4)
	require.Equal(t, 2, ssMap.summaryMap.Size())
	_, ok = ssMap.summaryMap.Get(key)
	require.True(t, ok)

	// Fifth statement has a different digest.
	stmtExecInfo5 := stmtExecInfo1
	stmtExecInfo5.Digest = "digest2"
	key = &StmtDigestKey{}
	key.Init(stmtExecInfo5.SchemaName, stmtExecInfo5.Digest, "", stmtExecInfo5.PlanDigest, stmtExecInfo5.ResourceGroupName)
	ssMap.AddStatement(stmtExecInfo5)
	require.Equal(t, 3, ssMap.summaryMap.Size())
	_, ok = ssMap.summaryMap.Get(key)
	require.True(t, ok)

	// Sixth statement has a different plan digest.
	stmtExecInfo6 := stmtExecInfo1
	stmtExecInfo6.PlanDigest = "plan_digest2"
	key = &StmtDigestKey{}
	key.Init(stmtExecInfo6.SchemaName, stmtExecInfo6.Digest, "", stmtExecInfo6.PlanDigest, stmtExecInfo6.ResourceGroupName)
	ssMap.AddStatement(stmtExecInfo6)
	require.Equal(t, 4, ssMap.summaryMap.Size())
	_, ok = ssMap.summaryMap.Get(key)
	require.True(t, ok)

	// Test for plan too large
	stmtExecInfo7 := stmtExecInfo1
	stmtExecInfo7.PlanDigest = "plan_digest7"
	buf := make([]byte, MaxEncodedPlanSizeInBytes+1)
	for i := range buf {
		buf[i] = 'a'
	}
	originalSQL := stmtExecInfo1.LazyInfo.GetOriginalSQL()
	stmtExecInfo7.LazyInfo = &mockLazyInfo{
		originalSQL: originalSQL,
		plan:        string(buf),
		hintStr:     "",
		binPlan:     "",
		planDigest:  "",
		bindingSQL:  originalSQL,
	}
	key = &StmtDigestKey{}
	key.Init(stmtExecInfo7.SchemaName, stmtExecInfo7.Digest, "", stmtExecInfo7.PlanDigest, stmtExecInfo7.ResourceGroupName)
	ssMap.AddStatement(stmtExecInfo7)
	require.Equal(t, 5, ssMap.summaryMap.Size())
	v, ok := ssMap.summaryMap.Get(key)
	require.True(t, ok)
	stmt := v.(*stmtSummaryByDigest)
	require.True(t, bytes.Contains(key.Hash(), hack.Slice(stmt.schemaName)))
	require.True(t, bytes.Contains(key.Hash(), hack.Slice(stmt.digest)))
	require.True(t, bytes.Contains(key.Hash(), hack.Slice(stmt.planDigest)))
	e := stmt.history.Back()
	ssElement := e.Value.(*stmtSummaryByDigestElement)
	require.Equal(t, plancodec.PlanDiscardedEncoded, ssElement.samplePlan)
}

func matchStmtSummaryByDigest(first, second *stmtSummaryByDigest) bool {
	if first.schemaName != second.schemaName ||
		first.digest != second.digest ||
		first.normalizedSQL != second.normalizedSQL ||
		first.planDigest != second.planDigest ||
		first.tableNames != second.tableNames ||
		!strings.EqualFold(first.stmtType, second.stmtType) {
		return false
	}
	if first.history.Len() != second.history.Len() {
		return false
	}
	ele1 := first.history.Front()
	ele2 := second.history.Front()
	for {
		if ele1 == nil {
			break
		}
		ssElement1 := ele1.Value.(*stmtSummaryByDigestElement)
		ssElement2 := ele2.Value.(*stmtSummaryByDigestElement)
		if ssElement1.beginTime != ssElement2.beginTime ||
			ssElement1.endTime != ssElement2.endTime ||
			ssElement1.sampleSQL != ssElement2.sampleSQL ||
			ssElement1.samplePlan != ssElement2.samplePlan ||
			ssElement1.prevSQL != ssElement2.prevSQL ||
			ssElement1.execCount != ssElement2.execCount ||
			ssElement1.sumErrors != ssElement2.sumErrors ||
			ssElement1.sumWarnings != ssElement2.sumWarnings ||
			ssElement1.sumLatency != ssElement2.sumLatency ||
			ssElement1.maxLatency != ssElement2.maxLatency ||
			ssElement1.minLatency != ssElement2.minLatency ||
			ssElement1.sumParseLatency != ssElement2.sumParseLatency ||
			ssElement1.maxParseLatency != ssElement2.maxParseLatency ||
			ssElement1.sumCompileLatency != ssElement2.sumCompileLatency ||
			ssElement1.maxCompileLatency != ssElement2.maxCompileLatency ||
			ssElement1.sumNumCopTasks != ssElement2.sumNumCopTasks ||
			ssElement1.sumCopProcessTime != ssElement2.sumCopProcessTime ||
			ssElement1.maxCopProcessTime != ssElement2.maxCopProcessTime ||
			ssElement1.maxCopProcessAddress != ssElement2.maxCopProcessAddress ||
			ssElement1.sumCopWaitTime != ssElement2.sumCopWaitTime ||
			ssElement1.maxCopWaitTime != ssElement2.maxCopWaitTime ||
			ssElement1.maxCopWaitAddress != ssElement2.maxCopWaitAddress ||
			ssElement1.sumProcessTime != ssElement2.sumProcessTime ||
			ssElement1.maxProcessTime != ssElement2.maxProcessTime ||
			ssElement1.sumWaitTime != ssElement2.sumWaitTime ||
			ssElement1.maxWaitTime != ssElement2.maxWaitTime ||
			ssElement1.sumBackoffTime != ssElement2.sumBackoffTime ||
			ssElement1.maxBackoffTime != ssElement2.maxBackoffTime ||
			ssElement1.sumTotalKeys != ssElement2.sumTotalKeys ||
			ssElement1.maxTotalKeys != ssElement2.maxTotalKeys ||
			ssElement1.sumProcessedKeys != ssElement2.sumProcessedKeys ||
			ssElement1.maxProcessedKeys != ssElement2.maxProcessedKeys ||
			ssElement1.sumGetCommitTsTime != ssElement2.sumGetCommitTsTime ||
			ssElement1.maxGetCommitTsTime != ssElement2.maxGetCommitTsTime ||
			ssElement1.sumPrewriteTime != ssElement2.sumPrewriteTime ||
			ssElement1.maxPrewriteTime != ssElement2.maxPrewriteTime ||
			ssElement1.sumCommitTime != ssElement2.sumCommitTime ||
			ssElement1.maxCommitTime != ssElement2.maxCommitTime ||
			ssElement1.sumLocalLatchTime != ssElement2.sumLocalLatchTime ||
			ssElement1.maxLocalLatchTime != ssElement2.maxLocalLatchTime ||
			ssElement1.sumCommitBackoffTime != ssElement2.sumCommitBackoffTime ||
			ssElement1.maxCommitBackoffTime != ssElement2.maxCommitBackoffTime ||
			ssElement1.sumResolveLockTime != ssElement2.sumResolveLockTime ||
			ssElement1.maxResolveLockTime != ssElement2.maxResolveLockTime ||
			ssElement1.sumWriteKeys != ssElement2.sumWriteKeys ||
			ssElement1.maxWriteKeys != ssElement2.maxWriteKeys ||
			ssElement1.sumWriteSize != ssElement2.sumWriteSize ||
			ssElement1.maxWriteSize != ssElement2.maxWriteSize ||
			ssElement1.sumPrewriteRegionNum != ssElement2.sumPrewriteRegionNum ||
			ssElement1.maxPrewriteRegionNum != ssElement2.maxPrewriteRegionNum ||
			ssElement1.sumTxnRetry != ssElement2.sumTxnRetry ||
			ssElement1.maxTxnRetry != ssElement2.maxTxnRetry ||
			ssElement1.sumBackoffTimes != ssElement2.sumBackoffTimes ||
			ssElement1.sumMem != ssElement2.sumMem ||
			ssElement1.maxMem != ssElement2.maxMem ||
			ssElement1.sumAffectedRows != ssElement2.sumAffectedRows ||
			!ssElement1.firstSeen.Equal(ssElement2.firstSeen) ||
			!ssElement1.lastSeen.Equal(ssElement2.lastSeen) ||
			ssElement1.resourceGroupName != ssElement2.resourceGroupName ||
			ssElement1.StmtRUSummary != ssElement2.StmtRUSummary ||
			ssElement1.StmtNetworkTrafficSummary != ssElement2.StmtNetworkTrafficSummary ||
			ssElement1.storageKV != ssElement2.storageKV ||
			ssElement1.storageMPP != ssElement2.storageMPP {
			return false
		}
		if len(ssElement1.backoffTypes) != len(ssElement2.backoffTypes) {
			return false
		}
		for key, value1 := range ssElement1.backoffTypes {
			value2, ok := ssElement2.backoffTypes[key]
			if !ok || value1 != value2 {
				return false
			}
		}
		if len(ssElement1.indexNames) != len(ssElement2.indexNames) {
			return false
		}
		for key, value1 := range ssElement1.indexNames {
			if value1 != ssElement2.indexNames[key] {
				return false
			}
		}
		ele1 = ele1.Next()
		ele2 = ele2.Next()
	}
	return true
}

func match(t *testing.T, row []types.Datum, expected ...any) {
	require.Equal(t, len(expected), len(row))
	for i := range row {
		got := fmt.Sprintf("%v", row[i].GetValue())
		need := fmt.Sprintf("%v", expected[i])
		require.Equal(t, need, got)
	}
}

func generateAnyExecInfo() *StmtExecInfo {
	tables := []stmtctx.TableEntry{{DB: "db1", Table: "tb1"}, {DB: "db2", Table: "tb2"}}
	indexes := []string{"a"}
	sc := stmtctx.NewStmtCtx()
	sc.StmtType = "Select"
	sc.Tables = tables
	sc.IndexNames = indexes
	sc.IsTiKV.Store(true)
	sc.IsTiFlash.Store(true)

	stmtExecInfo := &StmtExecInfo{
		SchemaName:     "schema_name",
		NormalizedSQL:  "normalized_sql",
		Digest:         "digest",
		PlanDigest:     "plan_digest",
		User:           "user",
		TotalLatency:   10000,
		ParseLatency:   100,
		CompileLatency: 1000,
		CopTasks: &execdetails.CopTasksSummary{
			NumCopTasks:       10,
			MaxProcessAddress: "127",
			MaxProcessTime:    15000,
			TotProcessTime:    10000,
			MaxWaitAddress:    "128",
			MaxWaitTime:       1500,
			TotWaitTime:       1000,
		},
		ExecDetail: execdetails.ExecDetails{
			BackoffTime:  80,
			RequestCount: 10,
			CommitDetail: &util.CommitDetails{
				GetCommitTsTime: 100,
				PrewriteTime:    10000,
				CommitTime:      1000,
				LocalLatchTime:  10,
				Mu: struct {
					sync.Mutex
					CommitBackoffTime    int64
					PrewriteBackoffTypes []string
					CommitBackoffTypes   []string
					SlowestPrewrite      util.ReqDetailInfo
					CommitPrimary        util.ReqDetailInfo
				}{
					CommitBackoffTime:    200,
					PrewriteBackoffTypes: []string{boTxnLockName},
					CommitBackoffTypes:   []string{},
					SlowestPrewrite:      util.ReqDetailInfo{},
					CommitPrimary:        util.ReqDetailInfo{},
				},
				WriteKeys:         20000,
				WriteSize:         200000,
				PrewriteRegionNum: 20,
				TxnRetry:          2,
				ResolveLock: util.ResolveLockDetail{
					ResolveLockTime: 2000,
				},
			},
			ScanDetail: &util.ScanDetail{
				TotalKeys:                 1000,
				ProcessedKeys:             500,
				RocksdbDeleteSkippedCount: 100,
				RocksdbKeySkippedCount:    10,
				RocksdbBlockCacheHitCount: 10,
				RocksdbBlockReadCount:     10,
				RocksdbBlockReadByte:      1000,
			},
			DetailsNeedP90: execdetails.DetailsNeedP90{
				TimeDetail: util.TimeDetail{
					ProcessTime: 500,
					WaitTime:    50,
				},
				CalleeAddress: "129",
			},
		},
		StmtCtx:           sc,
		MemMax:            10000,
		DiskMax:           10000,
		StartTime:         time.Date(2019, 1, 1, 10, 10, 10, 10, time.UTC),
		Succeed:           true,
		ResourceGroupName: "rg1",
		RUDetail:          util.NewRUDetailsWith(1.1, 2.5, 2*time.Millisecond),
		CPUUsages:         ppcpuusage.CPUUsages{TidbCPUTime: time.Duration(20), TikvCPUTime: time.Duration(100)},
		TiKVExecDetails: &util.ExecDetails{
			TrafficDetails: util.TrafficDetails{
				UnpackedBytesSentKVTotal:         10,
				UnpackedBytesReceivedKVTotal:     1000,
				UnpackedBytesReceivedKVCrossZone: 1,
				UnpackedBytesSentKVCrossZone:     100,
			},
		},
		LazyInfo: &mockLazyInfo{
			originalSQL:   "original_sql1",
			plan:          "",
			hintStr:       "",
			binPlan:       "",
			planDigest:    "",
			bindingSQL:    "binding_sql1",
			bindingDigest: "binding_digest1",
		},
	}
	stmtExecInfo.StmtCtx.AddAffectedRows(10000)
	return stmtExecInfo
}

type mockLazyInfo struct {
	originalSQL   string
	plan          string
	hintStr       string
	binPlan       string
	planDigest    string
	bindingSQL    string
	bindingDigest string
}

func (a *mockLazyInfo) GetOriginalSQL() string {
	return a.originalSQL
}

func (a *mockLazyInfo) GetEncodedPlan() (p string, h string, e any) {
	return a.plan, a.hintStr, nil
}

func (a *mockLazyInfo) GetBinaryPlan() string {
	return a.binPlan
}

func (a *mockLazyInfo) GetPlanDigest() string {
	return a.planDigest
}

func (a *mockLazyInfo) GetBindingSQLAndDigest() (s string, d string) {
	return a.bindingSQL, a.bindingDigest
}

func newStmtSummaryReaderForTest(ssMap *stmtSummaryByDigestMap) *stmtSummaryReader {
	columnNames := []string{
		SummaryBeginTimeStr,
		SummaryEndTimeStr,
		StmtTypeStr,
		SchemaNameStr,
		DigestStr,
		DigestTextStr,
		BindingDigestStr,
		BindingDigestTextStr,
		TableNamesStr,
		IndexNamesStr,
		SampleUserStr,
		ExecCountStr,
		SumErrorsStr,
		SumWarningsStr,
		SumLatencyStr,
		MaxLatencyStr,
		MinLatencyStr,
		AvgLatencyStr,
		AvgParseLatencyStr,
		MaxParseLatencyStr,
		AvgCompileLatencyStr,
		MaxCompileLatencyStr,
		SumCopTaskNumStr,
		MaxCopProcessTimeStr,
		MaxCopProcessAddressStr,
		MaxCopWaitTimeStr,
		MaxCopWaitAddressStr,
		AvgProcessTimeStr,
		MaxProcessTimeStr,
		AvgWaitTimeStr,
		MaxWaitTimeStr,
		AvgBackoffTimeStr,
		MaxBackoffTimeStr,
		AvgTotalKeysStr,
		MaxTotalKeysStr,
		AvgProcessedKeysStr,
		MaxProcessedKeysStr,
		AvgRocksdbDeleteSkippedCountStr,
		MaxRocksdbDeleteSkippedCountStr,
		AvgRocksdbKeySkippedCountStr,
		MaxRocksdbKeySkippedCountStr,
		AvgRocksdbBlockCacheHitCountStr,
		MaxRocksdbBlockCacheHitCountStr,
		AvgRocksdbBlockReadCountStr,
		MaxRocksdbBlockReadCountStr,
		AvgRocksdbBlockReadByteStr,
		MaxRocksdbBlockReadByteStr,
		AvgPrewriteTimeStr,
		MaxPrewriteTimeStr,
		AvgCommitTimeStr,
		MaxCommitTimeStr,
		AvgGetCommitTsTimeStr,
		MaxGetCommitTsTimeStr,
		AvgCommitBackoffTimeStr,
		MaxCommitBackoffTimeStr,
		AvgResolveLockTimeStr,
		MaxResolveLockTimeStr,
		AvgLocalLatchWaitTimeStr,
		MaxLocalLatchWaitTimeStr,
		AvgWriteKeysStr,
		MaxWriteKeysStr,
		AvgWriteSizeStr,
		MaxWriteSizeStr,
		AvgPrewriteRegionsStr,
		MaxPrewriteRegionsStr,
		AvgTxnRetryStr,
		MaxTxnRetryStr,
		SumExecRetryStr,
		SumExecRetryTimeStr,
		SumBackoffTimesStr,
		BackoffTypesStr,
		AvgMemStr,
		MaxMemStr,
		AvgDiskStr,
		MaxDiskStr,
		AvgKvTimeStr,
		AvgPdTimeStr,
		AvgBackoffTotalTimeStr,
		AvgWriteSQLRespTimeStr,
		MaxResultRowsStr,
		MinResultRowsStr,
		AvgResultRowsStr,
		PreparedStr,
		AvgAffectedRowsStr,
		FirstSeenStr,
		LastSeenStr,
		PlanInCacheStr,
		PlanCacheHitsStr,
		PlanInBindingStr,
		QuerySampleTextStr,
		PrevSampleTextStr,
		PlanDigestStr,
		PlanStr,
		AvgRequestUnitReadStr,
		MaxRequestUnitReadStr,
		AvgRequestUnitWriteStr,
		MaxRequestUnitWriteStr,
		AvgQueuedRcTimeStr,
		MaxQueuedRcTimeStr,
		ResourceGroupName,
		AvgTidbCPUTimeStr,
		AvgTikvCPUTimeStr,
		StorageKVStr,
		StorageMPPStr,
	}
	cols := make([]*model.ColumnInfo, len(columnNames))
	for i := range columnNames {
		cols[i] = &model.ColumnInfo{
			ID:     int64(i),
			Name:   ast.NewCIStr(columnNames[i]),
			Offset: i,
		}
	}
	reader := NewStmtSummaryReader(nil, true, cols, "", time.UTC)
	reader.ssMap = ssMap
	return reader
}

// Test stmtSummaryByDigest.ToDatum.
func TestToDatum(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()
	// to disable expiration
	ssMap.beginTimeForCurInterval = now + 60

	stmtExecInfo1 := generateAnyExecInfo()
	ssMap.AddStatement(stmtExecInfo1)
	reader := newStmtSummaryReaderForTest(ssMap)
	datums := reader.GetStmtSummaryCurrentRows()
	require.Equal(t, 1, len(datums))
	n := types.NewTime(types.FromGoTime(time.Unix(ssMap.beginTimeForCurInterval, 0).In(time.UTC)), mysql.TypeTimestamp, types.DefaultFsp)
	e := types.NewTime(types.FromGoTime(time.Unix(ssMap.beginTimeForCurInterval+1800, 0).In(time.UTC)), mysql.TypeTimestamp, types.DefaultFsp)
	f := types.NewTime(types.FromGoTime(stmtExecInfo1.StartTime), mysql.TypeTimestamp, types.DefaultFsp)
	isTiKV := 0
	if stmtExecInfo1.StmtCtx.IsTiKV.Load() {
		isTiKV = 1
	}
	isTiFlash := 0
	if stmtExecInfo1.StmtCtx.IsTiFlash.Load() {
		isTiFlash = 1
	}
	stmtExecInfo1.ExecDetail.CommitDetail.Mu.Lock()
	bindingSQL, bindingDigest := stmtExecInfo1.LazyInfo.GetBindingSQLAndDigest()
	expectedDatum := []any{n, e, "Select", stmtExecInfo1.SchemaName, stmtExecInfo1.Digest, stmtExecInfo1.NormalizedSQL, bindingDigest, bindingSQL,
		"db1.tb1,db2.tb2", "a", stmtExecInfo1.User, 1, 0, 0, int64(stmtExecInfo1.TotalLatency),
		int64(stmtExecInfo1.TotalLatency), int64(stmtExecInfo1.TotalLatency), int64(stmtExecInfo1.TotalLatency),
		int64(stmtExecInfo1.ParseLatency), int64(stmtExecInfo1.ParseLatency), int64(stmtExecInfo1.CompileLatency),
		int64(stmtExecInfo1.CompileLatency), stmtExecInfo1.CopTasks.NumCopTasks, int64(stmtExecInfo1.CopTasks.MaxProcessTime),
		stmtExecInfo1.CopTasks.MaxProcessAddress, int64(stmtExecInfo1.CopTasks.MaxWaitTime),
		stmtExecInfo1.CopTasks.MaxWaitAddress, int64(stmtExecInfo1.ExecDetail.TimeDetail.ProcessTime), int64(stmtExecInfo1.ExecDetail.TimeDetail.ProcessTime),
		int64(stmtExecInfo1.ExecDetail.TimeDetail.WaitTime), int64(stmtExecInfo1.ExecDetail.TimeDetail.WaitTime), int64(stmtExecInfo1.ExecDetail.BackoffTime),
		int64(stmtExecInfo1.ExecDetail.BackoffTime), stmtExecInfo1.ExecDetail.ScanDetail.TotalKeys, stmtExecInfo1.ExecDetail.ScanDetail.TotalKeys,
		stmtExecInfo1.ExecDetail.ScanDetail.ProcessedKeys, stmtExecInfo1.ExecDetail.ScanDetail.ProcessedKeys,
		int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbDeleteSkippedCount), int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbDeleteSkippedCount),
		int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbKeySkippedCount), int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbKeySkippedCount),
		int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockCacheHitCount), int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockCacheHitCount),
		int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockReadCount), int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockReadCount),
		int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockReadByte), int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockReadByte),
		int64(stmtExecInfo1.ExecDetail.CommitDetail.PrewriteTime), int64(stmtExecInfo1.ExecDetail.CommitDetail.PrewriteTime),
		int64(stmtExecInfo1.ExecDetail.CommitDetail.CommitTime), int64(stmtExecInfo1.ExecDetail.CommitDetail.CommitTime),
		int64(stmtExecInfo1.ExecDetail.CommitDetail.GetCommitTsTime), int64(stmtExecInfo1.ExecDetail.CommitDetail.GetCommitTsTime),
		stmtExecInfo1.ExecDetail.CommitDetail.Mu.CommitBackoffTime, stmtExecInfo1.ExecDetail.CommitDetail.Mu.CommitBackoffTime,
		stmtExecInfo1.ExecDetail.CommitDetail.ResolveLock.ResolveLockTime, stmtExecInfo1.ExecDetail.CommitDetail.ResolveLock.ResolveLockTime,
		int64(stmtExecInfo1.ExecDetail.CommitDetail.LocalLatchTime), int64(stmtExecInfo1.ExecDetail.CommitDetail.LocalLatchTime),
		stmtExecInfo1.ExecDetail.CommitDetail.WriteKeys, stmtExecInfo1.ExecDetail.CommitDetail.WriteKeys,
		stmtExecInfo1.ExecDetail.CommitDetail.WriteSize, stmtExecInfo1.ExecDetail.CommitDetail.WriteSize,
		stmtExecInfo1.ExecDetail.CommitDetail.PrewriteRegionNum, stmtExecInfo1.ExecDetail.CommitDetail.PrewriteRegionNum,
		stmtExecInfo1.ExecDetail.CommitDetail.TxnRetry, stmtExecInfo1.ExecDetail.CommitDetail.TxnRetry, 0, 0, 1,
		fmt.Sprintf("%s:1", boTxnLockName), stmtExecInfo1.MemMax, stmtExecInfo1.MemMax, stmtExecInfo1.DiskMax, stmtExecInfo1.DiskMax,
		0, 0, 0, 0, 0, 0, 0, 0, stmtExecInfo1.StmtCtx.AffectedRows(),
		f, f, 0, 0, 0, stmtExecInfo1.LazyInfo.GetOriginalSQL(), stmtExecInfo1.PrevSQL, "plan_digest", "", stmtExecInfo1.RUDetail.RRU(), stmtExecInfo1.RUDetail.RRU(),
		stmtExecInfo1.RUDetail.WRU(), stmtExecInfo1.RUDetail.WRU(), int64(stmtExecInfo1.RUDetail.RUWaitDuration()), int64(stmtExecInfo1.RUDetail.RUWaitDuration()),
		stmtExecInfo1.ResourceGroupName, int64(stmtExecInfo1.CPUUsages.TidbCPUTime), int64(stmtExecInfo1.CPUUsages.TikvCPUTime),
		isTiKV, isTiFlash}
	stmtExecInfo1.ExecDetail.CommitDetail.Mu.Unlock()
	match(t, datums[0], expectedDatum...)
	datums = reader.GetStmtSummaryHistoryRows()
	require.Equal(t, 1, len(datums))
	match(t, datums[0], expectedDatum...)

	// test evict
	err := ssMap.SetMaxStmtCount(1)
	defer func() {
		// clean up
		err = ssMap.SetMaxStmtCount(24)
		require.NoError(t, err)
	}()

	require.NoError(t, err)
	stmtExecInfo2 := stmtExecInfo1
	stmtExecInfo2.Digest = "bandit sei"
	ssMap.AddStatement(stmtExecInfo2)
	require.Equal(t, 1, ssMap.summaryMap.Size())
	datums = reader.GetStmtSummaryCurrentRows()
	expectedEvictedDatum := []any{n, e, "", "<nil>", "<nil>", "", "<nil>", "", "<nil>", "<nil>",
		stmtExecInfo1.User, 1, 0, 0, int64(stmtExecInfo1.TotalLatency),
		int64(stmtExecInfo1.TotalLatency), int64(stmtExecInfo1.TotalLatency), int64(stmtExecInfo1.TotalLatency),
		int64(stmtExecInfo1.ParseLatency), int64(stmtExecInfo1.ParseLatency), int64(stmtExecInfo1.CompileLatency),
		int64(stmtExecInfo1.CompileLatency), stmtExecInfo1.CopTasks.NumCopTasks, int64(stmtExecInfo1.CopTasks.MaxProcessTime),
		stmtExecInfo1.CopTasks.MaxProcessAddress, int64(stmtExecInfo1.CopTasks.MaxWaitTime),
		stmtExecInfo1.CopTasks.MaxWaitAddress, int64(stmtExecInfo1.ExecDetail.TimeDetail.ProcessTime), int64(stmtExecInfo1.ExecDetail.TimeDetail.ProcessTime),
		int64(stmtExecInfo1.ExecDetail.TimeDetail.WaitTime), int64(stmtExecInfo1.ExecDetail.TimeDetail.WaitTime), int64(stmtExecInfo1.ExecDetail.BackoffTime),
		int64(stmtExecInfo1.ExecDetail.BackoffTime), stmtExecInfo1.ExecDetail.ScanDetail.TotalKeys, stmtExecInfo1.ExecDetail.ScanDetail.TotalKeys,
		stmtExecInfo1.ExecDetail.ScanDetail.ProcessedKeys, stmtExecInfo1.ExecDetail.ScanDetail.ProcessedKeys,
		int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbDeleteSkippedCount), int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbDeleteSkippedCount),
		int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbKeySkippedCount), int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbKeySkippedCount),
		int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockCacheHitCount), int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockCacheHitCount),
		int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockReadCount), int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockReadCount),
		int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockReadByte), int64(stmtExecInfo1.ExecDetail.ScanDetail.RocksdbBlockReadByte),
		int64(stmtExecInfo1.ExecDetail.CommitDetail.PrewriteTime), int64(stmtExecInfo1.ExecDetail.CommitDetail.PrewriteTime),
		int64(stmtExecInfo1.ExecDetail.CommitDetail.CommitTime), int64(stmtExecInfo1.ExecDetail.CommitDetail.CommitTime),
		int64(stmtExecInfo1.ExecDetail.CommitDetail.GetCommitTsTime), int64(stmtExecInfo1.ExecDetail.CommitDetail.GetCommitTsTime),
		stmtExecInfo1.ExecDetail.CommitDetail.Mu.CommitBackoffTime, stmtExecInfo1.ExecDetail.CommitDetail.Mu.CommitBackoffTime,
		stmtExecInfo1.ExecDetail.CommitDetail.ResolveLock.ResolveLockTime, stmtExecInfo1.ExecDetail.CommitDetail.ResolveLock.ResolveLockTime,
		int64(stmtExecInfo1.ExecDetail.CommitDetail.LocalLatchTime), int64(stmtExecInfo1.ExecDetail.CommitDetail.LocalLatchTime),
		stmtExecInfo1.ExecDetail.CommitDetail.WriteKeys, stmtExecInfo1.ExecDetail.CommitDetail.WriteKeys,
		stmtExecInfo1.ExecDetail.CommitDetail.WriteSize, stmtExecInfo1.ExecDetail.CommitDetail.WriteSize,
		stmtExecInfo1.ExecDetail.CommitDetail.PrewriteRegionNum, stmtExecInfo1.ExecDetail.CommitDetail.PrewriteRegionNum,
		stmtExecInfo1.ExecDetail.CommitDetail.TxnRetry, stmtExecInfo1.ExecDetail.CommitDetail.TxnRetry, 0, 0, 1,
		fmt.Sprintf("%s:1", boTxnLockName), stmtExecInfo1.MemMax, stmtExecInfo1.MemMax, stmtExecInfo1.DiskMax, stmtExecInfo1.DiskMax,
		0, 0, 0, 0, 0, 0, 0, 0, stmtExecInfo1.StmtCtx.AffectedRows(),
		f, f, 0, 0, 0, "", "", "", "", stmtExecInfo1.RUDetail.RRU(), stmtExecInfo1.RUDetail.RRU(),
		stmtExecInfo1.RUDetail.WRU(), stmtExecInfo1.RUDetail.WRU(), int64(stmtExecInfo1.RUDetail.RUWaitDuration()), int64(stmtExecInfo1.RUDetail.RUWaitDuration()),
		stmtExecInfo1.ResourceGroupName, int64(stmtExecInfo1.CPUUsages.TidbCPUTime), int64(stmtExecInfo1.CPUUsages.TikvCPUTime),
		0, 0}
	expectedDatum[4] = stmtExecInfo2.Digest
	match(t, datums[0], expectedDatum...)
	match(t, datums[1], expectedEvictedDatum...)
}

// Test AddStatement and ToDatum parallel.
func TestAddStatementParallel(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()
	// to disable expiration
	ssMap.beginTimeForCurInterval = now + 60

	threads := 8
	loops := 32
	wg := sync.WaitGroup{}
	wg.Add(threads)

	reader := newStmtSummaryReaderForTest(ssMap)
	addStmtFunc := func() {
		defer wg.Done()
		stmtExecInfo1 := generateAnyExecInfo()

		// Add 32 times with different digest.
		for i := range loops {
			stmtExecInfo1.Digest = fmt.Sprintf("digest%d", i)
			ssMap.AddStatement(stmtExecInfo1)
		}

		// There would be 32 summaries.
		datums := reader.GetStmtSummaryCurrentRows()
		require.Len(t, datums, loops)
	}

	for range threads {
		go addStmtFunc()
	}
	wg.Wait()

	datums := reader.GetStmtSummaryCurrentRows()
	require.Len(t, datums, loops)
}

// Test max number of statement count.
func TestMaxStmtCount(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()
	// to disable expiration
	ssMap.beginTimeForCurInterval = now + 60

	// Test the original value and modify it.
	maxStmtCount := ssMap.maxStmtCount()
	require.Equal(t, 3000, maxStmtCount)
	require.Nil(t, ssMap.SetMaxStmtCount(10))
	require.Equal(t, 10, ssMap.maxStmtCount())
	defer func() {
		require.Nil(t, ssMap.SetMaxStmtCount(3000))
		require.Equal(t, 3000, maxStmtCount)
	}()

	// 100 digests
	stmtExecInfo1 := generateAnyExecInfo()
	loops := 100
	for i := range loops {
		stmtExecInfo1.Digest = fmt.Sprintf("digest%d", i)
		ssMap.AddStatement(stmtExecInfo1)
	}

	// Summary count should be MaxStmtCount.
	sm := ssMap.summaryMap
	require.Equal(t, 10, sm.Size())

	// LRU cache should work.
	for i := loops - 10; i < loops; i++ {
		key := &StmtDigestKey{}
		key.Init(stmtExecInfo1.SchemaName, fmt.Sprintf("digest%d", i), "", stmtExecInfo1.PlanDigest, stmtExecInfo1.ResourceGroupName)
		key.Hash()
		_, ok := sm.Get(key)
		require.True(t, ok)
	}

	// Change to a bigger value.
	require.Nil(t, ssMap.SetMaxStmtCount(50))
	for i := range loops {
		stmtExecInfo1.Digest = fmt.Sprintf("digest%d", i)
		ssMap.AddStatement(stmtExecInfo1)
	}
	require.Equal(t, 50, sm.Size())

	// Change to a smaller value.
	require.Nil(t, ssMap.SetMaxStmtCount(10))
	for i := range loops {
		stmtExecInfo1.Digest = fmt.Sprintf("digest%d", i)
		ssMap.AddStatement(stmtExecInfo1)
	}
	require.Equal(t, 10, sm.Size())
}

// Test max length of normalized and sample SQL.
func TestMaxSQLLength(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()
	// to disable expiration
	ssMap.beginTimeForCurInterval = now + 60

	// Test the original value and modify it.
	maxSQLLength := ssMap.maxSQLLength()
	require.Equal(t, 4096, maxSQLLength)

	// Create a long SQL
	length := maxSQLLength * 10
	str := strings.Repeat("a", length)

	stmtExecInfo1 := generateAnyExecInfo()
	stmtExecInfo1.LazyInfo.(*mockLazyInfo).originalSQL = str
	stmtExecInfo1.NormalizedSQL = str
	ssMap.AddStatement(stmtExecInfo1)

	key := &StmtDigestKey{}
	key.Init(stmtExecInfo1.SchemaName, stmtExecInfo1.Digest, "", stmtExecInfo1.PlanDigest, stmtExecInfo1.ResourceGroupName)
	value, ok := ssMap.summaryMap.Get(key)
	require.True(t, ok)

	expectedSQL := fmt.Sprintf("%s(len:%d)", strings.Repeat("a", maxSQLLength), length)
	summary := value.(*stmtSummaryByDigest)
	require.Equal(t, expectedSQL, summary.normalizedSQL)
	ssElement := summary.history.Back().Value.(*stmtSummaryByDigestElement)
	require.Equal(t, expectedSQL, ssElement.sampleSQL)

	require.Nil(t, ssMap.SetMaxSQLLength(100))
	require.Equal(t, 100, ssMap.maxSQLLength())
	require.Nil(t, ssMap.SetMaxSQLLength(10))
	require.Equal(t, 10, ssMap.maxSQLLength())
	require.Nil(t, ssMap.SetMaxSQLLength(4096))
	require.Equal(t, 4096, ssMap.maxSQLLength())
}

// Test AddStatement and SetMaxStmtCount parallel.
func TestSetMaxStmtCountParallel(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()
	// to disable expiration
	ssMap.beginTimeForCurInterval = now + 60

	threads := 8
	loops := 20
	var wg tidbutil.WaitGroupWrapper

	addStmtFunc := func() {
		stmtExecInfo1 := generateAnyExecInfo()

		// Add 32 times with different digest.
		for i := range loops {
			stmtExecInfo1.Digest = fmt.Sprintf("digest%d", i)
			ssMap.AddStatement(stmtExecInfo1)
		}
	}
	for range threads {
		wg.Run(addStmtFunc)
	}

	defer func() {
		require.NoError(t, ssMap.SetMaxStmtCount(3000))
	}()

	setStmtCountFunc := func() {
		// Turn down MaxStmtCount one by one.
		for i := 10; i > 0; i-- {
			require.NoError(t, ssMap.SetMaxStmtCount(uint(i)))
		}
	}
	wg.Run(setStmtCountFunc)

	wg.Wait()

	// add stmt again to make sure evict occurs after SetMaxStmtCount.
	addStmtFunc()

	reader := newStmtSummaryReaderForTest(ssMap)
	datums := reader.GetStmtSummaryCurrentRows()
	// due to evictions happened in cache, an additional record will be appended to the table.
	require.Equal(t, 2, len(datums))
}

// Test setting EnableStmtSummary to 0.
func TestDisableStmtSummary(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()

	err := ssMap.SetEnabled(false)
	require.NoError(t, err)
	ssMap.beginTimeForCurInterval = now + 60

	stmtExecInfo1 := generateAnyExecInfo()
	ssMap.AddStatement(stmtExecInfo1)
	reader := newStmtSummaryReaderForTest(ssMap)
	datums := reader.GetStmtSummaryCurrentRows()
	require.Len(t, datums, 0)

	err = ssMap.SetEnabled(true)
	require.NoError(t, err)

	ssMap.AddStatement(stmtExecInfo1)
	datums = reader.GetStmtSummaryCurrentRows()
	require.Equal(t, 1, len(datums))

	ssMap.beginTimeForCurInterval = now + 60

	stmtExecInfo2 := stmtExecInfo1
	stmtExecInfo2.LazyInfo.(*mockLazyInfo).originalSQL = "original_sql2"
	stmtExecInfo2.NormalizedSQL = "normalized_sql2"
	stmtExecInfo2.Digest = "digest2"
	ssMap.AddStatement(stmtExecInfo2)
	datums = reader.GetStmtSummaryCurrentRows()
	require.Equal(t, 2, len(datums))

	// Unset
	err = ssMap.SetEnabled(false)
	require.NoError(t, err)
	ssMap.beginTimeForCurInterval = now + 60
	ssMap.AddStatement(stmtExecInfo2)
	datums = reader.GetStmtSummaryCurrentRows()
	require.Len(t, datums, 0)

	// Unset
	err = ssMap.SetEnabled(false)
	require.NoError(t, err)

	err = ssMap.SetEnabled(true)
	require.NoError(t, err)

	ssMap.beginTimeForCurInterval = now + 60
	ssMap.AddStatement(stmtExecInfo1)
	datums = reader.GetStmtSummaryCurrentRows()
	require.Equal(t, 1, len(datums))

	// Set back.
	err = ssMap.SetEnabled(true)
	require.NoError(t, err)
}

// Test disable and enable statement summary concurrently with adding statements.
func TestEnableSummaryParallel(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()

	threads := 8
	loops := 32
	wg := sync.WaitGroup{}
	wg.Add(threads)

	reader := newStmtSummaryReaderForTest(ssMap)
	addStmtFunc := func() {
		defer wg.Done()
		stmtExecInfo1 := generateAnyExecInfo()

		// Add 32 times with same digest.
		for i := range loops {
			// Sometimes enable it and sometimes disable it.
			err := ssMap.SetEnabled(i%2 == 0)
			require.NoError(t, err)
			ssMap.AddStatement(stmtExecInfo1)
			// Try to read it.
			reader.GetStmtSummaryHistoryRows()
		}
		err := ssMap.SetEnabled(true)
		require.NoError(t, err)
	}

	for range threads {
		go addStmtFunc()
	}
	// Ensure that there's no deadlocks.
	wg.Wait()

	// Ensure that it's enabled at last.
	require.True(t, ssMap.Enabled())
}

// Test `formatBackoffTypes`.
func TestFormatBackoffTypes(t *testing.T) {
	backoffMap := make(map[string]int)
	require.Nil(t, formatBackoffTypes(backoffMap))
	bo1 := "pdrpc"
	backoffMap[bo1] = 1
	require.Equal(t, "pdrpc:1", formatBackoffTypes(backoffMap))
	bo2 := "txnlock"
	backoffMap[bo2] = 2

	require.Equal(t, "txnlock:2,pdrpc:1", formatBackoffTypes(backoffMap))
}

// Test refreshing current statement summary periodically.
func TestRefreshCurrentSummary(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()

	ssMap.beginTimeForCurInterval = now + 10
	stmtExecInfo1 := generateAnyExecInfo()
	key := &StmtDigestKey{}
	key.Init(stmtExecInfo1.SchemaName, stmtExecInfo1.Digest, "", stmtExecInfo1.PlanDigest, stmtExecInfo1.ResourceGroupName)
	ssMap.AddStatement(stmtExecInfo1)
	require.Equal(t, 1, ssMap.summaryMap.Size())
	value, ok := ssMap.summaryMap.Get(key)
	require.True(t, ok)
	ssElement := value.(*stmtSummaryByDigest).history.Back().Value.(*stmtSummaryByDigestElement)
	require.Equal(t, ssMap.beginTimeForCurInterval, ssElement.beginTime)
	require.Equal(t, int64(1), ssElement.execCount)

	ssMap.beginTimeForCurInterval = now - 1900
	ssElement.beginTime = now - 1900
	ssMap.AddStatement(stmtExecInfo1)
	require.Equal(t, 1, ssMap.summaryMap.Size())
	value, ok = ssMap.summaryMap.Get(key)
	require.True(t, ok)
	require.Equal(t, 2, value.(*stmtSummaryByDigest).history.Len())
	ssElement = value.(*stmtSummaryByDigest).history.Back().Value.(*stmtSummaryByDigestElement)
	require.Greater(t, ssElement.beginTime, now-1900)
	require.Equal(t, int64(1), ssElement.execCount)

	err := ssMap.SetRefreshInterval(10)
	require.NoError(t, err)
	ssMap.beginTimeForCurInterval = now - 20
	ssElement.beginTime = now - 20
	ssMap.AddStatement(stmtExecInfo1)
	require.Equal(t, 3, value.(*stmtSummaryByDigest).history.Len())
}

// Test expiring statement summary to history.
func TestSummaryHistory(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()
	err := ssMap.SetRefreshInterval(10)
	require.NoError(t, err)
	err = ssMap.SetHistorySize(10)
	require.NoError(t, err)
	defer func() {
		err := ssMap.SetRefreshInterval(1800)
		require.NoError(t, err)
	}()
	defer func() {
		err := ssMap.SetHistorySize(24)
		require.NoError(t, err)
	}()

	stmtExecInfo1 := generateAnyExecInfo()
	key := &StmtDigestKey{}
	key.Init(stmtExecInfo1.SchemaName, stmtExecInfo1.Digest, "", stmtExecInfo1.PlanDigest, stmtExecInfo1.ResourceGroupName)
	for i := range 11 {
		ssMap.beginTimeForCurInterval = now + int64(i+1)*10
		ssMap.AddStatement(stmtExecInfo1)
		require.Equal(t, 1, ssMap.summaryMap.Size())
		value, ok := ssMap.summaryMap.Get(key)
		require.True(t, ok)
		ssbd := value.(*stmtSummaryByDigest)
		if i < 10 {
			require.Equal(t, i+1, ssbd.history.Len())
			ssElement := ssbd.history.Back().Value.(*stmtSummaryByDigestElement)
			require.Equal(t, ssMap.beginTimeForCurInterval, ssElement.beginTime)
			require.Equal(t, int64(1), ssElement.execCount)
		} else {
			require.Equal(t, 10, ssbd.history.Len())
			ssElement := ssbd.history.Back().Value.(*stmtSummaryByDigestElement)
			require.Equal(t, ssMap.beginTimeForCurInterval, ssElement.beginTime)
			ssElement = ssbd.history.Front().Value.(*stmtSummaryByDigestElement)
			require.Equal(t, now+20, ssElement.beginTime)
		}
	}
	reader := newStmtSummaryReaderForTest(ssMap)
	datum := reader.GetStmtSummaryHistoryRows()
	require.Equal(t, 10, len(datum))

	err = ssMap.SetHistorySize(5)
	require.NoError(t, err)
	datum = reader.GetStmtSummaryHistoryRows()
	require.Equal(t, 5, len(datum))

	// test eviction
	ssMap.Clear()
	err = ssMap.SetMaxStmtCount(1)
	require.NoError(t, err)
	defer func() {
		err := ssMap.SetMaxStmtCount(3000)
		require.NoError(t, err)
	}()
	// insert first digest
	for i := range 6 {
		ssMap.beginTimeForCurInterval = now + int64(i)*10
		ssMap.AddStatement(stmtExecInfo1)
		require.Equal(t, 1, ssMap.summaryMap.Size())
		require.Equal(t, 0, ssMap.other.history.Len())
	}
	// insert another digest to evict it
	stmtExecInfo2 := stmtExecInfo1
	stmtExecInfo2.Digest = "bandit digest"
	ssMap.AddStatement(stmtExecInfo2)
	require.Equal(t, 1, ssMap.summaryMap.Size())
	// length of `other` should not longer than historySize.
	require.Equal(t, 5, ssMap.other.history.Len())
	datum = reader.GetStmtSummaryHistoryRows()
	// length of STATEMENT_SUMMARY_HISTORY == (history in cache) + (history evicted)
	require.Equal(t, 6, len(datum))
}

// Test summary when PrevSQL is not empty.
func TestPrevSQL(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()
	// to disable expiration
	ssMap.beginTimeForCurInterval = now + 60

	stmtExecInfo1 := generateAnyExecInfo()
	stmtExecInfo1.PrevSQL = "prevSQL"
	stmtExecInfo1.PrevSQLDigest = "prevSQLDigest"
	ssMap.AddStatement(stmtExecInfo1)
	key := &StmtDigestKey{}
	key.Init(stmtExecInfo1.SchemaName, stmtExecInfo1.Digest, stmtExecInfo1.PrevSQLDigest, stmtExecInfo1.PlanDigest, stmtExecInfo1.ResourceGroupName)
	require.Equal(t, 1, ssMap.summaryMap.Size())
	_, ok := ssMap.summaryMap.Get(key)
	require.True(t, ok)

	// same prevSQL
	ssMap.AddStatement(stmtExecInfo1)
	require.Equal(t, 1, ssMap.summaryMap.Size())

	// different prevSQL
	stmtExecInfo2 := stmtExecInfo1
	stmtExecInfo2.PrevSQL = "prevSQL1"
	stmtExecInfo2.PrevSQLDigest = "prevSQLDigest1"
	ssMap.AddStatement(stmtExecInfo2)
	require.Equal(t, 2, ssMap.summaryMap.Size())
	key.Init(stmtExecInfo2.SchemaName, stmtExecInfo2.Digest, stmtExecInfo2.PrevSQLDigest, stmtExecInfo2.PlanDigest, stmtExecInfo2.ResourceGroupName)
	_, ok = ssMap.summaryMap.Get(key)
	require.True(t, ok)
}

func TestEndTime(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()
	ssMap.beginTimeForCurInterval = now - 100

	stmtExecInfo1 := generateAnyExecInfo()
	ssMap.AddStatement(stmtExecInfo1)
	key := &StmtDigestKey{}
	key.Init(stmtExecInfo1.SchemaName, stmtExecInfo1.Digest, "", stmtExecInfo1.PlanDigest, stmtExecInfo1.ResourceGroupName)
	require.Equal(t, 1, ssMap.summaryMap.Size())
	value, ok := ssMap.summaryMap.Get(key)
	require.True(t, ok)
	ssbd := value.(*stmtSummaryByDigest)
	ssElement := ssbd.history.Back().Value.(*stmtSummaryByDigestElement)
	require.Equal(t, now-100, ssElement.beginTime)
	require.Equal(t, now+1700, ssElement.endTime)

	err := ssMap.SetRefreshInterval(3600)
	require.NoError(t, err)
	defer func() {
		err := ssMap.SetRefreshInterval(1800)
		require.NoError(t, err)
	}()
	ssMap.AddStatement(stmtExecInfo1)
	require.Equal(t, 1, ssbd.history.Len())
	ssElement = ssbd.history.Back().Value.(*stmtSummaryByDigestElement)
	require.Equal(t, now-100, ssElement.beginTime)
	require.Equal(t, now+3500, ssElement.endTime)

	err = ssMap.SetRefreshInterval(60)
	require.NoError(t, err)
	ssMap.AddStatement(stmtExecInfo1)
	require.Equal(t, 2, ssbd.history.Len())
	now2 := time.Now().Unix()
	ssElement = ssbd.history.Front().Value.(*stmtSummaryByDigestElement)
	require.Equal(t, now-100, ssElement.beginTime)
	require.GreaterOrEqual(t, ssElement.endTime, now)
	require.LessOrEqual(t, ssElement.endTime, now2)
	ssElement = ssbd.history.Back().Value.(*stmtSummaryByDigestElement)
	require.GreaterOrEqual(t, ssElement.beginTime, now-60)
	require.LessOrEqual(t, ssElement.beginTime, now2)
	require.Equal(t, int64(60), ssElement.endTime-ssElement.beginTime)
}

func TestPointGet(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()
	now := time.Now().Unix()
	ssMap.beginTimeForCurInterval = now - 100

	stmtExecInfo1 := generateAnyExecInfo()
	stmtExecInfo1.PlanDigest = ""
	stmtExecInfo1.LazyInfo.(*mockLazyInfo).plan = fakePlanDigestGenerator()
	ssMap.AddStatement(stmtExecInfo1)
	key := &StmtDigestKey{}
	key.Init(stmtExecInfo1.SchemaName, stmtExecInfo1.Digest, "", "", stmtExecInfo1.ResourceGroupName)
	require.Equal(t, 1, ssMap.summaryMap.Size())
	value, ok := ssMap.summaryMap.Get(key)
	require.True(t, ok)
	ssbd := value.(*stmtSummaryByDigest)
	ssElement := ssbd.history.Back().Value.(*stmtSummaryByDigestElement)
	require.Equal(t, int64(1), ssElement.execCount)

	ssMap.AddStatement(stmtExecInfo1)
	require.Equal(t, int64(2), ssElement.execCount)
}

func TestAccessPrivilege(t *testing.T) {
	ssMap := newStmtSummaryByDigestMap()

	loops := 32
	stmtExecInfo1 := generateAnyExecInfo()

	for i := range loops {
		stmtExecInfo1.Digest = fmt.Sprintf("digest%d", i)
		ssMap.AddStatement(stmtExecInfo1)
	}

	user := &auth.UserIdentity{Username: "user"}
	badUser := &auth.UserIdentity{Username: "bad_user"}

	reader := newStmtSummaryReaderForTest(ssMap)
	reader.user = user
	reader.hasProcessPriv = false
	datums := reader.GetStmtSummaryCurrentRows()
	require.Len(t, datums, loops)
	reader.user = badUser
	reader.hasProcessPriv = false
	datums = reader.GetStmtSummaryCurrentRows()
	require.Len(t, datums, 0)
	reader.hasProcessPriv = true
	datums = reader.GetStmtSummaryCurrentRows()
	require.Len(t, datums, loops)

	reader.user = user
	reader.hasProcessPriv = false
	datums = reader.GetStmtSummaryHistoryRows()
	require.Len(t, datums, loops)
	reader.user = badUser
	reader.hasProcessPriv = false
	datums = reader.GetStmtSummaryHistoryRows()
	require.Len(t, datums, 0)
	reader.hasProcessPriv = true
	datums = reader.GetStmtSummaryHistoryRows()
	require.Len(t, datums, loops)

	// Test the same query digests, but run as a different user in a new statement
	// summary interval. The old user should not be able to access the rows generated
	// for the new user.
	ssMap.beginTimeForCurInterval = time.Now().Unix()
	stmtExecInfo2 := generateAnyExecInfo()
	stmtExecInfo2.User = "new_user"

	for i := range loops {
		stmtExecInfo2.Digest = fmt.Sprintf("digest%d", i)
		ssMap.AddStatement(stmtExecInfo2)
	}

	oldUser := user
	newUser := &auth.UserIdentity{Username: "new_user"}

	reader.user = newUser
	reader.hasProcessPriv = false
	datums = reader.GetStmtSummaryCurrentRows()
	require.Len(t, datums, loops)
	reader.user = oldUser
	reader.hasProcessPriv = false
	datums = reader.GetStmtSummaryCurrentRows()
	require.Len(t, datums, 0)
	reader.user = oldUser
	reader.hasProcessPriv = true
	datums = reader.GetStmtSummaryCurrentRows()
	require.Len(t, datums, loops)
}
