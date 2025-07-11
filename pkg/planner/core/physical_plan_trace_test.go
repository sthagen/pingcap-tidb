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

package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/pingcap/tidb/pkg/domain"
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/planner/core"
	"github.com/pingcap/tidb/pkg/planner/core/base"
	"github.com/pingcap/tidb/pkg/planner/core/resolve"
	"github.com/pingcap/tidb/pkg/planner/core/rule"
	"github.com/pingcap/tidb/pkg/sessionctx"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/util/hint"
	"github.com/pingcap/tidb/pkg/util/tracing"
	"github.com/stretchr/testify/require"
)

func TestPhysicalOptimizeWithTraceEnabled(t *testing.T) {
	p := parser.New()
	store, dom := testkit.CreateMockStoreAndDomain(t)

	tk := testkit.NewTestKit(t, store)
	ctx := tk.Session().(sessionctx.Context)
	tk.MustExec("use test")
	tk.MustExec("create table t(a int primary key, b int, c int,d int,key ib (b),key ic (c))")
	tk.MustExec("SET session tidb_enable_index_merge = ON;")
	testcases := []struct {
		sql          string
		physicalList []string
	}{
		{
			sql: "select * from t",
			physicalList: []string{
				"TableFullScan_5", "TableReader_6", "Projection_3",
			},
		},
		{
			sql: "select max(b) from t",
			physicalList: []string{
				"IndexFullScan_28",
				"Limit_29",
				"IndexReader_30",
				"Limit_20",
				"StreamAgg_15",
				"Projection_8",
			},
		},
	}

	for _, testcase := range testcases {
		sql := testcase.sql
		stmt, err := p.ParseOneStmt(sql, "", "")
		require.NoError(t, err)
		nodeW := resolve.NewNodeW(stmt)
		err = core.Preprocess(context.Background(), ctx, nodeW, core.WithPreprocessorReturn(&core.PreprocessorReturn{InfoSchema: dom.InfoSchema()}))
		require.NoError(t, err)
		sctx := core.MockContext()
		sctx.GetSessionVars().StmtCtx.EnableOptimizeTrace = true
		sctx.GetSessionVars().CostModelVersion = 2
		builder, _ := core.NewPlanBuilder().Init(sctx, dom.InfoSchema(), hint.NewQBHintHandler(nil))
		domain.GetDomain(sctx).MockInfoCacheAndLoadInfoSchema(dom.InfoSchema())
		plan, err := builder.Build(context.TODO(), nodeW)
		require.NoError(t, err)
		plan.SCtx().GetSessionVars().StmtCtx.OriginalSQL = testcase.sql
		_, _, err = core.DoOptimize(context.TODO(), sctx, builder.GetOptFlag(), plan.(base.LogicalPlan))
		require.NoError(t, err)
		otrace := sctx.GetSessionVars().StmtCtx.OptimizeTracer.Physical
		require.NotNil(t, otrace)
		physicalList := getList(otrace)
		require.True(t, checkList(physicalList, testcase.physicalList))
		domain.GetDomain(sctx).StatsHandle().Close()
	}
}

func checkList(d []string, s []string) bool {
	if len(d) != len(s) {
		return false
	}
	for i := range d {
		if strings.Compare(d[i], s[i]) != 0 {
			return false
		}
	}
	return true
}

func getList(otrace *tracing.PhysicalOptimizeTracer) (pl []string) {
	for _, v := range otrace.Final {
		pl = append(pl, tracing.CodecPlanName(v.TP, v.ID))
	}
	return pl
}

// assert the case in https://github.com/pingcap/tidb/issues/34863
func TestPhysicalOptimizerTrace(t *testing.T) {
	p := parser.New()
	store, dom := testkit.CreateMockStoreAndDomain(t)

	tk := testkit.NewTestKit(t, store)
	ctx := tk.Session().(sessionctx.Context)
	tk.MustExec("use test")
	tk.MustExec("create table customer2(c_id bigint);")
	tk.MustExec("create table orders2(o_id bigint, c_id bigint);")
	tk.MustExec("insert into customer2 values(1),(2),(3),(4),(5);")
	tk.MustExec("insert into orders2 values(1,1),(2,1),(3,2),(4,2),(5,2);")
	tk.MustExec("set @@tidb_opt_agg_push_down=1;")

	sql := "select count(*) from customer2 c left join orders2 o on c.c_id=o.c_id;"

	stmt, err := p.ParseOneStmt(sql, "", "")
	require.NoError(t, err)
	nodeW := resolve.NewNodeW(stmt)
	err = core.Preprocess(context.Background(), ctx, nodeW, core.WithPreprocessorReturn(&core.PreprocessorReturn{InfoSchema: dom.InfoSchema()}))
	require.NoError(t, err)
	sctx := core.MockContext()
	defer func() {
		domain.GetDomain(sctx).StatsHandle().Close()
	}()
	sctx.GetSessionVars().StmtCtx.EnableOptimizeTrace = true
	sctx.GetSessionVars().AllowAggPushDown = true
	builder, _ := core.NewPlanBuilder().Init(sctx, dom.InfoSchema(), hint.NewQBHintHandler(nil))
	domain.GetDomain(sctx).MockInfoCacheAndLoadInfoSchema(dom.InfoSchema())
	plan, err := builder.Build(context.TODO(), nodeW)
	require.NoError(t, err)
	// flagGcSubstitute | flagStabilizeResults | flagSkewDistinctAgg | flagEliminateOuterJoin | flagPushDownAgg
	flag := rule.FlagGcSubstitute | rule.FlagStabilizeResults | rule.FlagSkewDistinctAgg | rule.FlagEliminateOuterJoin | rule.FlagPushDownAgg
	_, _, err = core.DoOptimize(context.TODO(), sctx, flag, plan.(base.LogicalPlan))
	require.NoError(t, err)
	otrace := sctx.GetSessionVars().StmtCtx.OptimizeTracer.Physical
	require.NotNil(t, otrace)
	elements := map[int]string{
		8:  "Projection",
		28: "HashAgg",
		16: "HashJoin",
		18: "HashJoin",
		17: "HashJoin",
		11: "Sort",
		15: "HashAgg",
		27: "TableFullScan",
		29: "TableReader",
		20: "HashAgg",
		24: "TableFullScan",
		25: "HashAgg",
		30: "TableFullScan",
		23: "HashAgg",
		21: "HashAgg",
		31: "TableReader",
		32: "TableFullScan",
		33: "TableReader",
	}
	for _, c := range otrace.Candidates {
		tp, ok := elements[c.ID]
		require.Truef(t, ok, "ID: %d not found in elements", c.ID)
		require.Equalf(t, tp, c.TP, "ID: %d, expected TP: %s, got TP: %s", c.ID, tp, c.TP)
	}
}

func TestPhysicalOptimizerTraceChildrenNotDuplicated(t *testing.T) {
	p := parser.New()
	store, dom := testkit.CreateMockStoreAndDomain(t)
	tk := testkit.NewTestKit(t, store)
	ctx := tk.Session().(sessionctx.Context)
	tk.MustExec("use test")
	tk.MustExec("create table t(it int);")
	sql := "select * from t"
	stmt, err := p.ParseOneStmt(sql, "", "")
	require.NoError(t, err)
	nodeW := resolve.NewNodeW(stmt)
	err = core.Preprocess(context.Background(), ctx, nodeW, core.WithPreprocessorReturn(&core.PreprocessorReturn{InfoSchema: dom.InfoSchema()}))
	require.NoError(t, err)
	sctx := core.MockContext()
	defer func() {
		domain.GetDomain(sctx).StatsHandle().Close()
	}()
	sctx.GetSessionVars().StmtCtx.EnableOptimizeTrace = true
	builder, _ := core.NewPlanBuilder().Init(sctx, dom.InfoSchema(), hint.NewQBHintHandler(nil))
	domain.GetDomain(sctx).MockInfoCacheAndLoadInfoSchema(dom.InfoSchema())
	plan, err := builder.Build(context.TODO(), nodeW)
	require.NoError(t, err)
	_, _, err = core.DoOptimize(context.TODO(), sctx, builder.GetOptFlag(), plan.(base.LogicalPlan))
	require.NoError(t, err)
	otrace := sctx.GetSessionVars().StmtCtx.OptimizeTracer.Physical
	for _, candidate := range otrace.Candidates {
		m := make(map[int]struct{})
		for _, childID := range candidate.ChildrenID {
			m[childID] = struct{}{}
		}
		require.Len(t, m, len(candidate.ChildrenID))
	}
}
